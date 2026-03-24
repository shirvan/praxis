package authservice

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// STSAPI abstracts the STS operations needed by the Auth Service.
// Tests inject a mock implementation; production code uses realSTSAPI.
type STSAPI interface {
	AssumeRole(ctx context.Context, roleARN string, opts AssumeRoleOpts) (*Credentials, error)
	GetCallerIdentity(ctx context.Context) (*CallerIdentity, error)
}

// AssumeRoleOpts configures an STS AssumeRole call.
type AssumeRoleOpts struct {
	ExternalID      string
	SessionDuration time.Duration
	SessionName     string
}

// Credentials is the result of a successful STS AssumeRole call.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ExpiresAt       time.Time
}

// CallerIdentity is the result of a GetCallerIdentity call.
type CallerIdentity struct {
	Account string
	ARN     string
	UserID  string
}

type realSTSAPI struct {
	client *sts.Client
}

// NewSTSAPI creates a production STS API wrapper from an aws.Config.
func NewSTSAPI(cfg aws.Config) STSAPI {
	return &realSTSAPI{client: sts.NewFromConfig(cfg)}
}

func (s *realSTSAPI) AssumeRole(ctx context.Context, roleARN string, opts AssumeRoleOpts) (*Credentials, error) {
	sessionName := opts.SessionName
	if sessionName == "" {
		sessionName = "praxis-auth-" + time.Now().UTC().Format("20060102T150405")
	}

	input := &sts.AssumeRoleInput{
		RoleArn:         aws.String(roleARN),
		RoleSessionName: aws.String(sessionName),
	}
	if opts.ExternalID != "" {
		input.ExternalId = aws.String(opts.ExternalID)
	}
	if opts.SessionDuration > 0 {
		input.DurationSeconds = aws.Int32(int32(opts.SessionDuration.Seconds()))
	}

	result, err := s.client.AssumeRole(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("sts:AssumeRole for %s: %w", roleARN, err)
	}

	return &Credentials{
		AccessKeyID:     aws.ToString(result.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(result.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(result.Credentials.SessionToken),
		ExpiresAt:       aws.ToTime(result.Credentials.Expiration),
	}, nil
}

func (s *realSTSAPI) GetCallerIdentity(ctx context.Context) (*CallerIdentity, error) {
	result, err := s.client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("sts:GetCallerIdentity: %w", err)
	}
	return &CallerIdentity{
		Account: aws.ToString(result.Account),
		ARN:     aws.ToString(result.Arn),
		UserID:  aws.ToString(result.UserId),
	}, nil
}
