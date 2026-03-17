package command

import (
	"errors"
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/registry"
	coretemplate "github.com/praxiscloud/praxis/internal/core/template"
	"github.com/praxiscloud/praxis/pkg/types"
)

// RegisterTemplate delegates template persistence to the registry object.
func (s *PraxisCommandService) RegisterTemplate(ctx restate.Context, req types.RegisterTemplateRequest) (types.RegisterTemplateResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	if strings.TrimSpace(req.Source) == "" {
		return types.RegisterTemplateResponse{}, restate.TerminalError(fmt.Errorf("template source is required"), 400)
	}
	return restate.Object[types.RegisterTemplateResponse](ctx, registry.TemplateRegistryServiceName, req.Name, "Register").Request(req)
}

// GetTemplate returns the full durable template record.
func (s *PraxisCommandService) GetTemplate(ctx restate.Context, name string) (types.TemplateRecord, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return types.TemplateRecord{}, restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	return restate.Object[types.TemplateRecord](ctx, registry.TemplateRegistryServiceName, trimmed, "Get").Request(restate.Void{})
}

// ListTemplates returns the global template listing.
func (s *PraxisCommandService) ListTemplates(ctx restate.Context, _ restate.Void) ([]types.TemplateSummary, error) {
	return restate.Object[[]types.TemplateSummary](ctx, registry.TemplateIndexServiceName, registry.TemplateIndexGlobalKey, "List").Request(restate.Void{})
}

// DeleteTemplate removes a template from the registry.
func (s *PraxisCommandService) DeleteTemplate(ctx restate.Context, req types.DeleteTemplateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return restate.TerminalError(fmt.Errorf("template name is required"), 400)
	}
	_, err := restate.WithRequestType[types.DeleteTemplateRequest, restate.Void](
		restate.Object[restate.Void](ctx, registry.TemplateRegistryServiceName, req.Name, "Delete"),
	).Request(req)
	return err
}

// ValidateTemplate performs static or full template validation.
func (s *PraxisCommandService) ValidateTemplate(ctx restate.Context, req types.ValidateTemplateRequest) (types.ValidateTemplateResponse, error) {
	mode := req.Mode
	if mode == "" {
		mode = types.ValidateModeStatic
	}

	switch mode {
	case types.ValidateModeStatic:
		source, _, err := s.resolveTemplateSource(ctx, req.Source, req.TemplateRef)
		if err != nil {
			return types.ValidateTemplateResponse{}, err
		}
		policies, err := s.loadAllPolicies(ctx, req.TemplateRef)
		if err != nil {
			return types.ValidateTemplateResponse{}, err
		}
		_, err = s.engine.EvaluateBytesWithPolicies([]byte(source), policies, req.Variables)
		if err != nil {
			return types.ValidateTemplateResponse{Valid: false, Errors: validationErrors(err)}, nil
		}
		return types.ValidateTemplateResponse{Valid: true}, nil
	case types.ValidateModeFull:
		account, err := s.resolveRequestAccount("", req.Variables)
		if err != nil {
			return types.ValidateTemplateResponse{Valid: false, Errors: validationErrors(restate.TerminalError(err, 400))}, nil
		}
		_, err = s.compileTemplate(ctx, req.Source, req.TemplateRef, req.Variables, account.Name)
		if err != nil {
			return types.ValidateTemplateResponse{Valid: false, Errors: validationErrors(err)}, nil
		}
		return types.ValidateTemplateResponse{Valid: true}, nil
	default:
		return types.ValidateTemplateResponse{}, restate.TerminalError(fmt.Errorf("invalid validation mode %q", mode), 400)
	}
}

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
	return []types.ValidationError{{
		Kind:    "Validation",
		Message: err.Error(),
	}}
}
