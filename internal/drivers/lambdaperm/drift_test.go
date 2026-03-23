package lambdaperm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPermissionHasDrift(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "events.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	assert.True(t, HasDrift(desired, observed))
}

func TestPermissionNoDrift(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	assert.False(t, HasDrift(desired, observed))
}
