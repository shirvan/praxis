// Package dashboard – aws.go
//
// This file contains the AWS API abstraction layer for AWS CloudWatch Dashboard.
// It defines the DashboardAPI interface (used for testing with mocks)
// and the real implementation that calls Amazon CloudWatch through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package dashboard

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	cloudwatch "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// DashboardAPI abstracts all Amazon CloudWatch SDK operations needed
// to manage a AWS CloudWatch Dashboard. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type DashboardAPI interface {
	PutDashboard(ctx context.Context, spec DashboardSpec) ([]ValidationMessage, error)
	GetDashboard(ctx context.Context, dashboardName string) (ObservedState, bool, error)
	DeleteDashboard(ctx context.Context, dashboardName string) error
}

type realDashboardAPI struct {
	client  *cloudwatch.Client
	limiter *ratelimit.Limiter
}

// NewDashboardAPI constructs a production DashboardAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewDashboardAPI(client *cloudwatch.Client) DashboardAPI {
	return &realDashboardAPI{
		client:  client,
		limiter: ratelimit.New("cloudwatch-dashboard", 20, 10),
	}
}

func (r *realDashboardAPI) PutDashboard(ctx context.Context, spec DashboardSpec) ([]ValidationMessage, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.PutDashboard(ctx, &cloudwatch.PutDashboardInput{
		DashboardName: aws.String(spec.DashboardName),
		DashboardBody: aws.String(spec.DashboardBody),
	})
	if err != nil {
		return nil, err
	}
	messages := make([]ValidationMessage, 0, len(out.DashboardValidationMessages))
	for _, message := range out.DashboardValidationMessages {
		messages = append(messages, ValidationMessage{
			DataPath: aws.ToString(message.DataPath),
			Message:  aws.ToString(message.Message),
		})
	}
	return messages, nil
}

// GetDashboard reads the current state of the AWS CloudWatch Dashboard from Amazon CloudWatch.
func (r *realDashboardAPI) GetDashboard(ctx context.Context, dashboardName string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.GetDashboard(ctx, &cloudwatch.GetDashboardInput{DashboardName: aws.String(dashboardName)})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	return ObservedState{
		DashboardArn:  aws.ToString(out.DashboardArn),
		DashboardName: aws.ToString(out.DashboardName),
		DashboardBody: aws.ToString(out.DashboardBody),
	}, true, nil
}

// DeleteDashboard removes the AWS CloudWatch Dashboard from AWS via Amazon CloudWatch.
func (r *realDashboardAPI) DeleteDashboard(ctx context.Context, dashboardName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteDashboards(ctx, &cloudwatch.DeleteDashboardsInput{DashboardNames: []string{dashboardName}})
	return err
}

// IsNotFound returns true if the AWS error indicates the AWS CloudWatch Dashboard does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFound", "DashboardNotFoundError")
}

func IsDashboardInvalidInput(err error) bool {
	return awserr.HasCode(err, "DashboardInvalidInputError")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue")
}

func IsThrottled(err error) bool {
	return awserr.IsThrottled(err)
}

var _ cwtypes.DashboardValidationMessage
