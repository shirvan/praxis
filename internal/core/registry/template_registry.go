package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"cuelang.org/go/cue/cuecontext"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// TemplateRegistry stores one durable record per template name.
type TemplateRegistry struct{}

func (TemplateRegistry) ServiceName() string {
	return TemplateRegistryServiceName
}

func (TemplateRegistry) Register(ctx restate.ObjectContext, req types.RegisterTemplateRequest) (types.RegisterTemplateResponse, error) {
	existing, err := restate.Get[*types.TemplateRecord](ctx, stateKey)
	if err != nil {
		return types.RegisterTemplateResponse{}, err
	}

	now, err := restate.Run(ctx, func(runCtx restate.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
	if err != nil {
		return types.RegisterTemplateResponse{}, err
	}

	record, summary, resp, err := registerTemplateRecord(restate.Key(ctx), existing, req, now)
	if err != nil {
		return types.RegisterTemplateResponse{}, err
	}

	restate.Set(ctx, stateKey, record)
	restate.ObjectSend(ctx, TemplateIndexServiceName, TemplateIndexGlobalKey, "Upsert").Send(summary)
	return resp, nil
}

func (TemplateRegistry) Delete(ctx restate.ObjectContext, req types.DeleteTemplateRequest) error {
	existing, err := restate.Get[*types.TemplateRecord](ctx, stateKey)
	if err != nil {
		return err
	}
	name, err := deleteTemplateRecord(existing, req)
	if err != nil {
		return err
	}
	restate.Clear(ctx, stateKey)
	restate.ObjectSend(ctx, TemplateIndexServiceName, TemplateIndexGlobalKey, "Remove").Send(name)
	return nil
}

func (TemplateRegistry) Get(ctx restate.ObjectSharedContext, _ restate.Void) (types.TemplateRecord, error) {
	record, err := restate.Get[*types.TemplateRecord](ctx, stateKey)
	if err != nil {
		return types.TemplateRecord{}, err
	}
	if record == nil {
		return types.TemplateRecord{}, restate.TerminalError(fmt.Errorf("template %q not found", restate.Key(ctx)), 404)
	}
	return *record, nil
}

func (TemplateRegistry) GetSource(ctx restate.ObjectSharedContext, _ restate.Void) (string, error) {
	record, err := restate.Get[*types.TemplateRecord](ctx, stateKey)
	if err != nil {
		return "", err
	}
	if record == nil {
		return "", restate.TerminalError(fmt.Errorf("template %q not found", restate.Key(ctx)), 404)
	}
	return record.Source, nil
}

func (TemplateRegistry) GetVariableSchema(ctx restate.ObjectSharedContext, _ restate.Void) (types.VariableSchema, error) {
	record, err := restate.Get[*types.TemplateRecord](ctx, stateKey)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, restate.TerminalError(fmt.Errorf("template %q not found", restate.Key(ctx)), 404)
	}
	return record.VariableSchema, nil
}

func registerTemplateRecord(key string, existing *types.TemplateRecord, req types.RegisterTemplateRequest, now time.Time) (types.TemplateRecord, types.TemplateSummary, types.RegisterTemplateResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return types.TemplateRecord{}, types.TemplateSummary{}, types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	if key != "" && key != name {
		return types.TemplateRecord{}, types.TemplateSummary{}, types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template name %q does not match object key %q", name, key), 400)
	}
	rawSource := req.Source
	if strings.TrimSpace(rawSource) == "" {
		return types.TemplateRecord{}, types.TemplateSummary{}, types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template source is required"), 400)
	}
	if compiled := cuecontext.New().CompileString(rawSource); compiled.Err() != nil {
		return types.TemplateRecord{}, types.TemplateSummary{}, types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("invalid CUE source: %w", compiled.Err()), 400)
	}

	record := types.TemplateRecord{}
	createdAt := now
	if existing != nil {
		record = *existing
		createdAt = existing.Metadata.CreatedAt
		if strings.TrimSpace(existing.Source) != "" {
			record.PreviousSource = existing.Source
			record.PreviousDigest = existing.Digest
		}
	}

	digest := templateDigest(rawSource)
	record.Metadata.Name = name
	if req.Description != "" || existing == nil {
		record.Metadata.Description = req.Description
	}
	if req.Labels != nil || existing == nil {
		record.Metadata.Labels = cloneLabels(req.Labels)
	}
	record.Metadata.CreatedAt = createdAt
	record.Metadata.UpdatedAt = now
	record.Source = rawSource
	record.Digest = digest

	// Extract variable schema from CUE source.
	variableSchema, schemaErr := template.ExtractVariableSchema([]byte(rawSource))
	if schemaErr != nil {
		return types.TemplateRecord{}, types.TemplateSummary{}, types.RegisterTemplateResponse{},
			restate.TerminalError(fmt.Errorf("failed to extract variable schema: %w", schemaErr), 400)
	}
	record.VariableSchema = variableSchema

	summary := types.TemplateSummary{
		Name:        name,
		Description: record.Metadata.Description,
		UpdatedAt:   now,
	}
	resp := types.RegisterTemplateResponse{Name: name, Digest: digest}
	return record, summary, resp, nil
}

func deleteTemplateRecord(existing *types.TemplateRecord, req types.DeleteTemplateRequest) (string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	if existing == nil {
		return "", restate.TerminalError(fmt.Errorf("template %q not found", name), 404)
	}
	return name, nil
}

func templateDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}
