package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// VerificationResult holds the result of verifying generated CUE.
type VerificationResult struct {
	ParseOK      bool                `json:"parseOk"`
	ParseErrors  []string            `json:"parseErrors,omitempty"`
	SchemaOK     bool                `json:"schemaOk"`
	SchemaErrors []string            `json:"schemaErrors,omitempty"`
	PlanOK       bool                `json:"planOk"`
	PlanErrors   []string            `json:"planErrors,omitempty"`
	PlanResult   *types.PlanResponse `json:"planResult,omitempty"`
}

// VerifyMigrationOutput checks the generated CUE for correctness by running
// a plan dry-run through PraxisCommandService.
func VerifyMigrationOutput(ctx restate.Context, cueSource string) (*VerificationResult, error) {
	result := &VerificationResult{}

	// Run a dry-run plan — this validates CUE syntax, schema conformance,
	// and resource references all in one shot.
	planResp, err := restate.Service[types.PlanResponse](
		ctx, "PraxisCommandService", "Plan",
	).Request(types.PlanRequest{
		Template: cueSource,
	})
	if err != nil {
		errMsg := err.Error()
		// Classify the error.
		result.ParseErrors = append(result.ParseErrors, errMsg)
		return result, nil
	}

	result.ParseOK = true
	result.SchemaOK = true
	result.PlanOK = true
	result.PlanResult = &planResp
	return result, nil
}

// FormatVerificationErrors returns a human-readable string of verification errors.
func FormatVerificationErrors(v *VerificationResult) string {
	var s string
	if len(v.ParseErrors) > 0 {
		s += "Parse errors:\n"
		for _, e := range v.ParseErrors {
			s += "  - " + e + "\n"
		}
	}
	if len(v.SchemaErrors) > 0 {
		s += "Schema errors:\n"
		for _, e := range v.SchemaErrors {
			s += "  - " + e + "\n"
		}
	}
	if len(v.PlanErrors) > 0 {
		s += "Plan errors:\n"
		for _, e := range v.PlanErrors {
			s += "  - " + e + "\n"
		}
	}
	return s
}

// FormatVerificationResult returns a summary of a successful verification.
func FormatVerificationResult(v *VerificationResult) string {
	if v.PlanResult != nil {
		result, _ := json.MarshalIndent(v.PlanResult, "", "  ")
		return fmt.Sprintf("Verification passed. Plan result:\n%s", string(result))
	}
	return "Verification passed."
}
