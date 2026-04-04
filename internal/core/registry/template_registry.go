package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
	"time"

	"cuelang.org/go/cue/cuecontext"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// TemplateRegistry is a Restate Virtual Object that stores one durable CUE
// template record per template name. Each template is keyed by its name,
// meaning Restate routes all operations for the same template name to the
// same Virtual Object instance, providing serialized access and consistent
// state.
//
// Storage layout:
//   - State key "record" holds the full types.TemplateRecord (source, digest,
//     metadata, variable schema, previous version for rollback).
//   - On register, the index VO (TemplateIndex) is notified via a one-way
//     message to keep the global template listing in sync.
type TemplateRegistry struct{}

// ServiceName returns the Restate service name used to register this Virtual Object.
func (TemplateRegistry) ServiceName() string {
	return TemplateRegistryServiceName
}

// Register creates or updates a template record. This is an exclusive handler
// (ObjectContext) so concurrent writes to the same template name are serialized
// by Restate.
//
// The method:
//  1. Reads the existing record (if any) from Restate state.
//  2. Captures the current time via restate.Run for deterministic journaling.
//  3. Validates and compiles the CUE source, extracts the variable schema.
//  4. Persists the new record and sends an async update to the TemplateIndex.
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

// Delete removes a template record and notifies the index to drop it from
// the global listing. Returns a TerminalError(404) if the template does not
// exist, stopping Restate retries immediately.
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

// Get retrieves the full template record. This is a shared (read-only) handler
// so it does not block concurrent reads on the same key. Returns a
// TerminalError(404) if the template has never been registered.
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

// GetSource returns just the raw CUE source text. This is a lightweight shared
// handler used by the command service and template engine when only the source
// is needed (e.g. for evaluation), avoiding the cost of deserializing the full
// record with metadata and variable schema.
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

// GetMetadata returns the template's metadata (name, description, labels,
// timestamps) without the full source or variable schema. Used by the
// concierge to describe a template without loading the entire record.
func (TemplateRegistry) GetMetadata(ctx restate.ObjectSharedContext, _ restate.Void) (types.TemplateMetadata, error) {
	record, err := restate.Get[*types.TemplateRecord](ctx, stateKey)
	if err != nil {
		return types.TemplateMetadata{}, err
	}
	if record == nil {
		return types.TemplateMetadata{}, restate.TerminalError(fmt.Errorf("template %q not found", restate.Key(ctx)), 404)
	}
	return record.Metadata, nil
}

// GetVariableSchema returns the extracted variable schema for the template.
// The CLI uses this to validate user-provided variables before evaluation and
// to generate interactive prompts for required fields.
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

// registerTemplateRecord is a pure function (no Restate context) that builds
// the new template record, template summary, and response from inputs. This
// separation makes the logic unit-testable without a Restate runtime.
//
// Validation steps:
//  1. Name must be non-empty and match the VO key.
//  2. CUE source must be non-empty and syntactically valid.
//  3. Variable schema is extracted from the CUE source for runtime validation.
//  4. If updating an existing record, the previous source and digest are preserved
//     for rollback support.
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

// deleteTemplateRecord validates the delete request. Returns the template name
// on success so the caller can notify the index.
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

// templateDigest computes a SHA-256 hex digest of the CUE source. This is
// stored alongside the record and returned to clients so they can detect
// whether a template has changed without comparing the full source text.
func templateDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// cloneLabels creates a defensive copy of a label map to avoid shared
// mutation between the request and the stored record.
func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	maps.Copy(cloned, labels)
	return cloned
}
