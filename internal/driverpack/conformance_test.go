package driverpack_test

import (
	"fmt"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/provider"
	computepack "github.com/shirvan/praxis/internal/driverpack/compute"
	identitypack "github.com/shirvan/praxis/internal/driverpack/identity"
	monitoringpack "github.com/shirvan/praxis/internal/driverpack/monitoring"
	networkpack "github.com/shirvan/praxis/internal/driverpack/network"
	storagepack "github.com/shirvan/praxis/internal/driverpack/storage"
)

const expectedDriverCount = 51

var expectedGenericDrivers = map[string]struct{}{
	"ALB":                  {},
	"ACMCertificate":       {},
	"AMI":                  {},
	"AuroraCluster":        {},
	"Dashboard":            {},
	"DBParameterGroup":     {},
	"DBSubnetGroup":        {},
	"DynamoDBTable":        {},
	"EC2Instance":          {},
	"ECSCluster":           {},
	"EKSCluster":           {},
	"EBSVolume":            {},
	"ElasticIP":            {},
	"ECRLifecyclePolicy":   {},
	"ECRRepository":        {},
	"EventSourceMapping":   {},
	"IAMGroup":             {},
	"IAMInstanceProfile":   {},
	"IAMPolicy":            {},
	"IAMRole":              {},
	"IAMUser":              {},
	"InternetGateway":      {},
	"KeyPair":              {},
	"KMSKey":               {},
	"LambdaFunction":       {},
	"LambdaLayer":          {},
	"LambdaPermission":     {},
	"Listener":             {},
	"ListenerRule":         {},
	"LogGroup":             {},
	"MetricAlarm":          {},
	"NATGateway":           {},
	"NetworkACL":           {},
	"NLB":                  {},
	"RDSInstance":          {},
	"RouteTable":           {},
	"Route53HostedZone":    {},
	"Route53HealthCheck":   {},
	"Route53Record":        {},
	"S3Bucket":             {},
	"SecurityGroup":        {},
	"SecretsManagerSecret": {},
	"SNSSubscription":      {},
	"SNSTopic":             {},
	"SQSQueue":             {},
	"SQSQueuePolicy":       {},
	"SSMParameter":         {},
	"Subnet":               {},
	"TargetGroup":          {},
	"VPC":                  {},
	"VPCPeeringConnection": {},
}

type packInventory struct {
	name        string
	expected    int
	definitions []restate.ServiceDefinition
}

func productionPacks() []packInventory {
	return []packInventory{
		{name: "compute", expected: 11, definitions: computepack.Definitions(nil)},
		{name: "identity", expected: 7, definitions: identitypack.Definitions(nil)},
		{name: "monitoring", expected: 3, definitions: monitoringpack.Definitions(nil)},
		{name: "network", expected: 18, definitions: networkpack.Definitions(nil)},
		{name: "storage", expected: 12, definitions: storagepack.Definitions(nil)},
	}
}

// TestProductionDriverInventoryMatchesProviderRegistry ties together the two
// production dispatch boundaries: services actually bound by driver binaries
// and adapters addressable by Core. A driver present on only one side is
// unusable, even though both packages can compile independently.
func TestProductionDriverInventoryMatchesProviderRegistry(t *testing.T) {
	definitions := make(map[string]restate.ServiceDefinition, expectedDriverCount)
	for _, pack := range productionPacks() {
		t.Run(pack.name, func(t *testing.T) {
			require.Len(t, pack.definitions, pack.expected)
			for _, definition := range pack.definitions {
				require.NotNil(t, definition)
				name := definition.Name()
				require.NotEmpty(t, name)
				_, duplicate := definitions[name]
				require.Falsef(t, duplicate, "driver service %q is bound by more than one pack", name)
				definitions[name] = definition
			}
		})
	}
	require.Len(t, definitions, expectedDriverCount)

	adapters := provider.NewRegistry(nil).All()
	require.Len(t, adapters, expectedDriverCount)

	for kind, adapter := range adapters {
		t.Run(kind, func(t *testing.T) {
			require.Equal(t, kind, adapter.Kind())
			require.Equal(t, kind, adapter.ServiceName(), "kind and Restate service name must stay identical")
			require.Contains(t, definitions, adapter.ServiceName(), "Core adapter has no production driver binding")
		})
	}

	for name := range definitions {
		t.Run(name+"/adapter", func(t *testing.T) {
			_, ok := adapters[name]
			require.Truef(t, ok, "bound driver service %q has no Core adapter", name)
		})
	}
}

type handlerContract struct {
	mode      string
	hasInput  bool
	hasOutput bool
}

var requiredHandlers = map[string]handlerContract{
	"Provision":  {mode: "EXCLUSIVE", hasInput: true, hasOutput: true},
	"Import":     {mode: "EXCLUSIVE", hasInput: true, hasOutput: true},
	"Delete":     {mode: "EXCLUSIVE"},
	"Reconcile":  {mode: "EXCLUSIVE", hasOutput: true},
	"GetStatus":  {mode: "SHARED", hasOutput: true},
	"GetOutputs": {mode: "SHARED", hasOutput: true},
	"GetInputs":  {mode: "SHARED", hasOutput: true},
	"ClearState": {mode: "EXCLUSIVE"},
}

// TestProductionDriversImplementLifecycleContract checks the reflected API,
// not merely Go method names. It catches invalid signatures that Reflect would
// silently omit, shared/exclusive regressions, and accidental payload changes.
func TestProductionDriversImplementLifecycleContract(t *testing.T) {
	for _, pack := range productionPacks() {
		for _, definition := range pack.definitions {
			t.Run(pack.name+"/"+definition.Name(), func(t *testing.T) {
				assert.Equal(t, "VIRTUAL_OBJECT", fmt.Sprint(definition.Type()))
				require.NotNil(t, definition.GetOptions().InvocationRetryPolicy, "production binding must use the standard bounded retry policy")

				handlers := definition.Handlers()
				for name, contract := range requiredHandlers {
					handler, ok := handlers[name]
					require.Truef(t, ok, "required lifecycle handler %q is not reflected", name)
					assertHandlerContract(
						t,
						name,
						fmt.Sprint(*handler.HandlerType()),
						handler.InputPayload().ContentType != nil,
						handler.OutputPayload().ContentType != nil,
						contract,
					)
				}

				for name := range handlers {
					if _, required := requiredHandlers[name]; required {
						continue
					}
					require.Failf(t, "unexpected public driver handler", "handler %q is not part of the lifecycle contract", name)
				}
			})
		}
	}
}

// TestGenericDriverBindings prevents a production driver from silently moving
// away from the one supported lifecycle implementation.
func TestGenericDriverBindings(t *testing.T) {
	seen := map[string]struct{}{}
	for _, pack := range productionPacks() {
		for _, definition := range pack.definitions {
			_, marked := definition.(interface{ GenericLifecycleBinding() })
			_, expected := expectedGenericDrivers[definition.Name()]
			assert.Equalf(t, expected, marked, "%s generic lifecycle binding", definition.Name())
			if marked {
				seen[definition.Name()] = struct{}{}
			}
		}
	}
	assert.Equal(t, expectedGenericDrivers, seen)
}

func assertHandlerContract(t *testing.T, name, mode string, hasInput, hasOutput bool, contract handlerContract) {
	t.Helper()
	assert.Equal(t, contract.mode, mode, "%s handler concurrency mode", name)
	assert.Equal(t, contract.hasInput, hasInput, "%s handler input payload", name)
	assert.Equal(t, contract.hasOutput, hasOutput, "%s handler output payload", name)
}
