package provider

import (
	"context"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbpgdriver "github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	lambdapermdriver "github.com/shirvan/praxis/internal/drivers/lambdaperm"
	route53recorddriver "github.com/shirvan/praxis/internal/drivers/route53record"
	secretdriver "github.com/shirvan/praxis/internal/drivers/secret"
	ssmdriver "github.com/shirvan/praxis/internal/drivers/ssmparameter"
)

type dbParameterGroupLookupAPIStub struct {
	dbpgdriver.DBParameterGroupAPI
	observed  dbpgdriver.ObservedState
	name      string
	groupType string
}

func (s *dbParameterGroupLookupAPIStub) DescribeParameterGroup(_ context.Context, name, groupType string) (dbpgdriver.ObservedState, error) {
	s.name, s.groupType = name, groupType
	return s.observed, nil
}

type lambdaPermissionLookupAPIStub struct {
	lambdapermdriver.PermissionAPI
	observed lambdapermdriver.ObservedState
}

func (s lambdaPermissionLookupAPIStub) GetPermission(context.Context, string, string) (lambdapermdriver.ObservedState, error) {
	return s.observed, nil
}

type route53RecordLookupAPIStub struct {
	route53recorddriver.RecordAPI
	observed route53recorddriver.ObservedState
	identity route53recorddriver.RecordIdentity
}

func (s *route53RecordLookupAPIStub) DescribeRecord(_ context.Context, identity route53recorddriver.RecordIdentity) (route53recorddriver.ObservedState, error) {
	s.identity = identity
	return s.observed, nil
}

type secretMetadataLookupAPIStub struct {
	observed secretdriver.ObservedState
}

func (s secretMetadataLookupAPIStub) DescribeSecretMetadata(context.Context, string) (secretdriver.ObservedState, bool, error) {
	return s.observed, true, nil
}

type ssmMetadataLookupAPIStub struct {
	observed ssmdriver.ObservedState
	names    []string
}

func (s *ssmMetadataLookupAPIStub) DescribeParameterMetadata(_ context.Context, name string) (ssmdriver.ObservedState, bool, error) {
	s.names = append(s.names, name)
	return s.observed, name == s.observed.ParameterName, nil
}

func TestDBParameterGroupLookupProbe_ParsesClusterARN(t *testing.T) {
	arn := "arn:aws:rds:us-west-2:123456789012:cluster-pg:payments"
	api := &dbParameterGroupLookupAPIStub{observed: dbpgdriver.ObservedState{
		GroupName: "payments", ARN: arn, Family: "aurora-postgresql16", Type: dbpgdriver.TypeCluster,
		Tags: map[string]string{"env": "prod"},
	}}
	outputs, found, err := dbParameterGroupLookupProbe(api)(nil, LookupFilter{ID: arn, Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "payments", api.name)
	assert.Equal(t, dbpgdriver.TypeCluster, api.groupType)
	assert.Equal(t, arn, outputs.ARN)
}

func TestDBParameterGroupLookupProbe_RejectsName(t *testing.T) {
	_, _, err := dbParameterGroupLookupProbe(&dbParameterGroupLookupAPIStub{})(nil, LookupFilter{Name: "payments"})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
}

func TestLambdaPermissionLookupProbe_UsesCompositeIdentity(t *testing.T) {
	probe := lambdaPermissionLookupProbe(lambdaPermissionLookupAPIStub{observed: lambdapermdriver.ObservedState{
		StatementId: "allow-s3", FunctionName: "processor", Principal: "s3.amazonaws.com", Action: "lambda:InvokeFunction",
	}})
	outputs, found, err := probe(nil, LookupFilter{ID: "processor~allow-s3"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "processor", outputs.FunctionName)
	assert.JSONEq(t, `{"Sid":"allow-s3","Principal":"s3.amazonaws.com","Action":"lambda:InvokeFunction"}`, outputs.Statement)
}

func TestRoute53RecordLookupProbe_UsesImportIdentity(t *testing.T) {
	api := &route53RecordLookupAPIStub{observed: route53recorddriver.ObservedState{
		HostedZoneId: "Z123", Name: "api.example.com", Type: "A", SetIdentifier: "west",
	}}
	outputs, found, err := route53RecordLookupProbe(api)(nil, LookupFilter{ID: "Z123/api.example.com./a/west"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "api.example.com", api.identity.Name)
	assert.Equal(t, "A", api.identity.Type)
	assert.Equal(t, "west", outputs.SetIdentifier)
}

func TestSecretsManagerLookupProbe_ReturnsMetadataOnlyOutput(t *testing.T) {
	probe := secretsManagerSecretLookupProbe(secretMetadataLookupAPIStub{observed: secretdriver.ObservedState{
		ARN: "arn:aws:secretsmanager:us-west-2:123:secret:payments", Name: "payments", VersionID: "version-1",
		Tags: map[string]string{"env": "prod"},
	}})
	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "version-1", outputs.VersionID)
}

func TestSSMParameterLookupProbe_ResolvesARNWithoutReadingValue(t *testing.T) {
	arn := "arn:aws:ssm:us-west-2:123:parameter/praxis/payments"
	api := &ssmMetadataLookupAPIStub{observed: ssmdriver.ObservedState{
		ARN: arn, ParameterName: "/praxis/payments", Type: "SecureString", Version: 4, Tier: "Standard",
	}}
	outputs, found, err := ssmParameterLookupProbe(api)(nil, LookupFilter{ID: arn})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []string{"praxis/payments", "/praxis/payments"}, api.names)
	assert.Equal(t, int64(4), outputs.Version)
}
