// handlers_template.go implements the template registry CRUD handlers.
//
// Templates in Praxis are CUE configurations that describe desired cloud
// infrastructure. They can be used inline (Apply/Plan) or registered in
// a durable template registry for reuse (Deploy/PlanDeploy).
//
// The command service acts as a thin gateway: it validates inputs, then
// delegates the actual persistence to the TemplateRegistryObj virtual object
// (keyed by template name). This keeps the command service stateless while
// the virtual object owns all durable template state.
//
// Template validation supports two modes:
//   - Static: evaluates the CUE template with policies but does not call
//     any cloud provider APIs.
//   - Full: runs the complete compileTemplate pipeline including data source
//     resolution and SSM parameter substitution.
package command

import (
	"errors"
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/registry"
	coretemplate "github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// RegisterTemplate delegates template persistence to the registry object.
// The registry object stores the CUE source, extracts the variable schema,
// and maintains an index entry for listing.
func (s *PraxisCommandService) RegisterTemplate(ctx restate.Context, req types.RegisterTemplateRequest) (types.RegisterTemplateResponse, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	if strings.TrimSpace(req.Source) == "" {
		return types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template source is required"), 400)
	}
	// Delegate to the TemplateRegistryObj "Register" handler. This acquires
	// the per-key lock (exclusive handler) to prevent concurrent writes.
	return restate.Object[types.RegisterTemplateResponse](ctx, registry.TemplateRegistryServiceName, req.Name, "Register").Request(req)
}

// GetTemplate returns the full durable template record including source,
// variable schema, and metadata.
func (s *PraxisCommandService) GetTemplate(ctx restate.Context, name string) (types.TemplateRecord, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return types.TemplateRecord{}, restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	return restate.Object[types.TemplateRecord](ctx, registry.TemplateRegistryServiceName, trimmed, "Get").Request(restate.Void{})
}

// ListTemplates returns the global template listing from the index virtual
// object. The index is a single-key object that tracks all registered
// template names and their summary metadata.
func (s *PraxisCommandService) ListTemplates(ctx restate.Context, _ restate.Void) ([]types.TemplateSummary, error) {
	return restate.Object[[]types.TemplateSummary](ctx, registry.TemplateIndexServiceName, registry.TemplateIndexGlobalKey, "List").Request(restate.Void{})
}

// DeleteTemplate removes a template from the registry. The registry object
// handles both removing its own state and updating the global index.
func (s *PraxisCommandService) DeleteTemplate(ctx restate.Context, req types.DeleteTemplateRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	_, err := restate.WithRequestType[types.DeleteTemplateRequest, restate.Void](
		restate.Object[restate.Void](ctx, registry.TemplateRegistryServiceName, req.Name, "Delete"),
	).Request(req)
	return err
}

// ValidateTemplate performs static or full template validation without
// creating any durable state.
//
// Modes:
//   - "static": Evaluates the CUE template with policies but skips all
//     cloud API calls (data source resolution, SSM, adapter integration).
//     Good for CI linting.
//   - "full": Runs the complete compileTemplate pipeline. Requires valid
//     AWS credentials and network access. Good for pre-deploy validation.
func (s *PraxisCommandService) ValidateTemplate(ctx restate.Context, req types.ValidateTemplateRequest) (types.ValidateTemplateResponse, error) {
	mode := req.Mode
	if mode == "" {
		mode = types.ValidateModeStatic
	}

	switch mode {
	case types.ValidateModeStatic:
		// Resolve the template source (inline or from registry).
		source, _, err := s.resolveTemplateSource(ctx, req.Source, req.TemplateRef, "")
		if err != nil {
			return types.ValidateTemplateResponse{}, err
		}
		// Load policies that would apply during evaluation.
		policies, err := s.loadAllPolicies(ctx, req.TemplateRef)
		if err != nil {
			return types.ValidateTemplateResponse{}, err
		}
		// Evaluate the template. Any CUE or policy errors are returned
		// as structured validation errors rather than handler errors.
		_, err = s.engine.EvaluateBytesWithPolicies([]byte(source), policies, req.Variables)
		if err != nil {
			return types.ValidateTemplateResponse{Valid: false, Errors: validationErrors(err)}, nil
		}
		return types.ValidateTemplateResponse{Valid: true}, nil
	case types.ValidateModeFull:
		// Full validation runs the entire compile pipeline.
		account, err := s.resolveRequestAccount("", req.Variables)
		if err != nil {
			return types.ValidateTemplateResponse{Valid: false, Errors: validationErrors(restate.TerminalError(err, 400))}, nil
		}
		_, err = s.compileTemplate(ctx, req.Source, req.TemplateRef, req.Variables, account, nil, "")
		if err != nil {
			return types.ValidateTemplateResponse{Valid: false, Errors: validationErrors(err)}, nil
		}
		return types.ValidateTemplateResponse{Valid: true}, nil
	default:
		return types.ValidateTemplateResponse{}, restate.TerminalError(fmt.Errorf("invalid validation mode %q (supported: %q, %q)", mode, types.ValidateModeStatic, types.ValidateModeFull), 400)
	}
}

// validationErrors converts a Go error into a structured list of
// ValidationError values suitable for returning in a ValidateTemplateResponse.
// If the error is a TemplateErrors composite (from the CUE engine), each
// individual error is extracted with its path, kind, and policy context.
func validationErrors(err error) []types.ValidationError {
	var templateErrs coretemplate.TemplateErrors
	if errors.As(err, &templateErrs) {
		out := make([]types.ValidationError, 0, len(templateErrs))
		for _, item := range templateErrs {
			out = append(out, types.ValidationError{
				Kind:    item.Kind.String(),
				Path:    item.Path,
				Message: item.Message,
				Detail:  item.Detail,
				Policy:  item.PolicyName,
			})
		}
		return out
	}
	// Fallback for non-structured errors.
	return []types.ValidationError{{
		Kind:    "Validation",
		Message: err.Error(),
	}}
}
