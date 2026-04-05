package concierge

import "strings"

// resourceTypeMap maps source format resource types to Praxis resource kinds.
// This is the canonical mapping used during migration to translate external IaC
// resource types to their Praxis equivalents. The map covers three source formats:
//
//	Terraform:       aws_s3_bucket          → S3Bucket
//	CloudFormation:  AWS::S3::Bucket        → S3Bucket
//	Crossplane:      Bucket                 → S3Bucket
//
// Resources not found in this map are reported as UnmappedTypes in the migration
// inventory, and the LLM is warned to skip or approximate them.
var resourceTypeMap = map[string]string{
	// Terraform → Praxis
	"aws_s3_bucket":                   "S3Bucket",
	"aws_security_group":              "SecurityGroup",
	"aws_vpc":                         "VPC",
	"aws_subnet":                      "Subnet",
	"aws_internet_gateway":            "InternetGateway",
	"aws_nat_gateway":                 "NATGateway",
	"aws_route_table":                 "RouteTable",
	"aws_network_acl":                 "NetworkACL",
	"aws_eip":                         "ElasticIP",
	"aws_instance":                    "EC2Instance",
	"aws_key_pair":                    "KeyPair",
	"aws_ami":                         "AMI",
	"aws_ebs_volume":                  "EBSVolume",
	"aws_iam_role":                    "IAMRole",
	"aws_iam_policy":                  "IAMPolicy",
	"aws_iam_user":                    "IAMUser",
	"aws_iam_group":                   "IAMGroup",
	"aws_iam_instance_profile":        "IAMInstanceProfile",
	"aws_route53_zone":                "Route53HostedZone",
	"aws_route53_record":              "Route53Record",
	"aws_route53_health_check":        "Route53HealthCheck",
	"aws_db_instance":                 "RDSInstance",
	"aws_rds_cluster":                 "AuroraCluster",
	"aws_db_subnet_group":             "DBSubnetGroup",
	"aws_db_parameter_group":          "DBParameterGroup",
	"aws_lambda_function":             "LambdaFunction",
	"aws_lambda_layer_version":        "LambdaLayer",
	"aws_lambda_permission":           "LambdaPermission",
	"aws_lambda_event_source_mapping": "EventSourceMapping",
	"aws_lb":                          "ALB",
	"aws_lb_target_group":             "TargetGroup",
	"aws_lb_listener":                 "Listener",
	"aws_lb_listener_rule":            "ListenerRule",
	"aws_vpc_peering_connection":      "VPCPeeringConnection",
	"aws_sns_topic":                   "SNSTopic",
	"aws_sns_topic_subscription":      "SNSSubscription",
	"aws_sqs_queue":                   "SQSQueue",
	"aws_sqs_queue_policy":            "SQSQueuePolicy",
	"aws_ecr_repository":              "ECRRepository",
	"aws_ecr_lifecycle_policy":        "ECRLifecyclePolicy",
	"aws_acm_certificate":             "ACMCertificate",
	"aws_cloudwatch_log_group":        "LogGroup",
	"aws_cloudwatch_metric_alarm":     "MetricAlarm",
	"aws_cloudwatch_dashboard":        "Dashboard",

	// CloudFormation → Praxis
	"AWS::S3::Bucket":                           "S3Bucket",
	"AWS::EC2::SecurityGroup":                   "SecurityGroup",
	"AWS::EC2::VPC":                             "VPC",
	"AWS::EC2::Subnet":                          "Subnet",
	"AWS::EC2::InternetGateway":                 "InternetGateway",
	"AWS::EC2::NatGateway":                      "NATGateway",
	"AWS::EC2::RouteTable":                      "RouteTable",
	"AWS::EC2::NetworkAcl":                      "NetworkACL",
	"AWS::EC2::EIP":                             "ElasticIP",
	"AWS::EC2::Instance":                        "EC2Instance",
	"AWS::EC2::KeyPair":                         "KeyPair",
	"AWS::EC2::Volume":                          "EBSVolume",
	"AWS::EC2::VPCPeeringConnection":            "VPCPeeringConnection",
	"AWS::IAM::Role":                            "IAMRole",
	"AWS::IAM::Policy":                          "IAMPolicy",
	"AWS::IAM::User":                            "IAMUser",
	"AWS::IAM::Group":                           "IAMGroup",
	"AWS::IAM::InstanceProfile":                 "IAMInstanceProfile",
	"AWS::Route53::HostedZone":                  "Route53HostedZone",
	"AWS::Route53::RecordSet":                   "Route53Record",
	"AWS::Route53::HealthCheck":                 "Route53HealthCheck",
	"AWS::RDS::DBInstance":                      "RDSInstance",
	"AWS::RDS::DBCluster":                       "AuroraCluster",
	"AWS::RDS::DBSubnetGroup":                   "DBSubnetGroup",
	"AWS::RDS::DBParameterGroup":                "DBParameterGroup",
	"AWS::Lambda::Function":                     "LambdaFunction",
	"AWS::Lambda::LayerVersion":                 "LambdaLayer",
	"AWS::Lambda::Permission":                   "LambdaPermission",
	"AWS::Lambda::EventSourceMapping":           "EventSourceMapping",
	"AWS::ElasticLoadBalancingV2::LoadBalancer": "ALB",
	"AWS::ElasticLoadBalancingV2::TargetGroup":  "TargetGroup",
	"AWS::ElasticLoadBalancingV2::Listener":     "Listener",
	"AWS::ElasticLoadBalancingV2::ListenerRule": "ListenerRule",
	"AWS::SNS::Topic":                           "SNSTopic",
	"AWS::SNS::Subscription":                    "SNSSubscription",
	"AWS::SQS::Queue":                           "SQSQueue",
	"AWS::SQS::QueuePolicy":                     "SQSQueuePolicy",
	"AWS::ECR::Repository":                      "ECRRepository",
	"AWS::CertificateManager::Certificate":      "ACMCertificate",
	"AWS::Logs::LogGroup":                       "LogGroup",
	"AWS::CloudWatch::Alarm":                    "MetricAlarm",
	"AWS::CloudWatch::Dashboard":                "Dashboard",

	// Crossplane → Praxis (AWS provider only)
	"Bucket":          "S3Bucket",
	"SecurityGroup":   "SecurityGroup",
	"VPC":             "VPC",
	"Subnet":          "Subnet",
	"InternetGateway": "InternetGateway",
	"NATGateway":      "NATGateway",
	"RouteTable":      "RouteTable",
	"Instance":        "EC2Instance",
	"Role":            "IAMRole",
	"Policy":          "IAMPolicy",
}

// LookupPraxisKind returns the Praxis kind for a source resource type.
// Returns false if the source type has no known Praxis equivalent.
func LookupPraxisKind(sourceType string) (string, bool) {
	kind, ok := resourceTypeMap[sourceType]
	return kind, ok
}

// FormatMappingTable returns a human-readable mapping table included in the
// migration prompt so the LLM knows which source types map to which Praxis kinds.
func FormatMappingTable() string {
	var sb strings.Builder
	sb.WriteString("Source Type → Praxis Kind\n")
	for src, kind := range resourceTypeMap {
		sb.WriteString(src)
		sb.WriteString(" → ")
		sb.WriteString(kind)
		sb.WriteString("\n")
	}
	return sb.String()
}
