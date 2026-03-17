//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/core/command"
	"github.com/praxiscloud/praxis/pkg/types"
)

func TestTemplateRegistry_RegisterGetListAndApplyByRef(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "tmplref")
	templateName := "shared-s3-template"
	templateBody := simpleS3Template(bucketName)

	registerResp, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:        templateName,
		Source:      templateBody,
		Description: "shared s3 template",
		Labels:      map[string]string{"team": "platform"},
	})
	require.NoError(t, err)
	assert.Equal(t, templateName, registerResp.Name)
	assert.NotEmpty(t, registerResp.Digest)

	record, err := ingress.Service[string, types.TemplateRecord](
		env.ingress, "PraxisCommandService", "GetTemplate",
	).Request(t.Context(), templateName)
	require.NoError(t, err)
	assert.Equal(t, templateName, record.Metadata.Name)
	assert.Equal(t, templateBody, record.Source)

	summaries, err := ingress.Service[restate.Void, []types.TemplateSummary](
		env.ingress, "PraxisCommandService", "ListTemplates",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	require.NotEmpty(t, summaries)
	assert.Equal(t, templateName, summaries[0].Name)

	planResp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		TemplateRef: &types.TemplateRef{Name: templateName},
		Variables:   accountVariables(),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, planResp.Plan.Summary.ToCreate)

	applyResp, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		TemplateRef:   &types.TemplateRef{Name: templateName},
		DeploymentKey: "template-ref-apply-" + bucketName,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, applyResp.DeploymentKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		60*time.Second,
	)
	assert.Equal(t, types.DeploymentComplete, state.Status)

	detail, err := ingress.Object[restate.Void, *types.DeploymentDetail](
		env.ingress, "DeploymentStateObj", applyResp.DeploymentKey, "GetDetail",
	).Request(context.Background(), restate.Void{})
	require.NoError(t, err)
	require.NotNil(t, detail)
	assert.Equal(t, "registry://"+templateName, detail.TemplatePath)
}

func TestTemplateRegistry_DeleteRemovesTemplate(t *testing.T) {
	env := setupCoreStack(t)
	templateName := "delete-me"

	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: "resources: {}",
	})
	require.NoError(t, err)

	_, err = ingress.Service[command.DeleteTemplateRequest, restate.Void](
		env.ingress, "PraxisCommandService", "DeleteTemplate",
	).Request(t.Context(), command.DeleteTemplateRequest{Name: templateName})
	require.NoError(t, err)

	_, err = ingress.Service[string, types.TemplateRecord](
		env.ingress, "PraxisCommandService", "GetTemplate",
	).Request(t.Context(), templateName)
	require.Error(t, err)
}
