package nacl

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// NetworkACLAPI abstracts the AWS EC2 SDK operations for Network ACLs.
// The real implementation wraps the SDK with rate limiting. Rule operations
// (Create/Delete/ReplaceEntry) modify individual numbered entries rather than
// replacing the entire rule set atomically.
type NetworkACLAPI interface {
	CreateNetworkACL(ctx context.Context, spec NetworkACLSpec) (string, error)                                   // Creates a NACL with managed-key tag.
	DescribeNetworkACL(ctx context.Context, networkAclId string) (ObservedState, error)                          // Fetches live state; filters rule 32767 and IPv6.
	DeleteNetworkACL(ctx context.Context, networkAclId string) error                                             // Deletes the NACL; fails if subnets still associated.
	CreateEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error                // Adds a numbered rule entry.
	DeleteEntry(ctx context.Context, networkAclId string, ruleNumber int, egress bool) error                     // Removes a numbered rule entry.
	ReplaceEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error               // Replaces an existing rule in-place by number.
	ReplaceNetworkACLAssociation(ctx context.Context, associationId string, networkAclId string) (string, error) // Moves a subnet to a different NACL.
	UpdateTags(ctx context.Context, networkAclId string, tags map[string]string) error                           // Replaces all user tags.
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)                                     // Finds NACL by praxis:managed-key tag.
	FindAssociationIdForSubnet(ctx context.Context, subnetId string) (string, error)                             // Finds the current NACL association for a subnet.
	FindDefaultNetworkACL(ctx context.Context, vpcId string) (string, error)                                     // Finds the VPC's default NACL (used during delete).
}

// realNetworkACLAPI implements NetworkACLAPI using the actual AWS SDK v2 EC2 client.
type realNetworkACLAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter // Token-bucket: 20 burst, 10/s refill.
}

// NewNetworkACLAPI creates a new NetworkACLAPI backed by the given EC2 SDK client.
func NewNetworkACLAPI(client *ec2sdk.Client) NetworkACLAPI {
	return &realNetworkACLAPI{
		client:  client,
		limiter: ratelimit.New("network-acl", 20, 10),
	}
}

func (r *realNetworkACLAPI) CreateNetworkACL(ctx context.Context, spec NetworkACLSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		tags = append(tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}

	out, err := r.client.CreateNetworkAcl(ctx, &ec2sdk.CreateNetworkAclInput{
		VpcId: aws.String(spec.VpcId),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeNetworkAcl,
			Tags:         tags,
		}},
	})
	if err != nil {
		return "", err
	}
	if out.NetworkAcl == nil {
		return "", fmt.Errorf("CreateNetworkAcl returned nil network ACL")
	}
	return aws.ToString(out.NetworkAcl.NetworkAclId), nil
}

func (r *realNetworkACLAPI) DescribeNetworkACL(ctx context.Context, networkAclId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeNetworkAcls(ctx, &ec2sdk.DescribeNetworkAclsInput{
		NetworkAclIds: []string{networkAclId},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.NetworkAcls) == 0 {
		return ObservedState{}, fmt.Errorf("network ACL %s not found", networkAclId)
	}

	acl := out.NetworkAcls[0]
	observed := ObservedState{
		NetworkAclId: aws.ToString(acl.NetworkAclId),
		VpcId:        aws.ToString(acl.VpcId),
		IsDefault:    aws.ToBool(acl.IsDefault),
		Tags:         make(map[string]string, len(acl.Tags)),
	}
	for _, tag := range acl.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	for _, entry := range acl.Entries {
		rule, include := networkACLRuleFromEntry(entry)
		if !include {
			continue
		}
		if aws.ToBool(entry.Egress) {
			observed.EgressRules = append(observed.EgressRules, rule)
		} else {
			observed.IngressRules = append(observed.IngressRules, rule)
		}
	}
	for _, association := range acl.Associations {
		subnetID := aws.ToString(association.SubnetId)
		if subnetID == "" {
			continue
		}
		observed.Associations = append(observed.Associations, NetworkACLAssociation{
			AssociationId: aws.ToString(association.NetworkAclAssociationId),
			SubnetId:      subnetID,
		})
	}
	sortRules(observed.IngressRules)
	sortRules(observed.EgressRules)
	sortAssociations(observed.Associations)
	return observed, nil
}

func (r *realNetworkACLAPI) DeleteNetworkACL(ctx context.Context, networkAclId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteNetworkAcl(ctx, &ec2sdk.DeleteNetworkAclInput{
		NetworkAclId: aws.String(networkAclId),
	})
	return err
}

func (r *realNetworkACLAPI) CreateEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input, err := networkACLEntryInput(networkAclId, rule, egress)
	if err != nil {
		return err
	}
	_, err = r.client.CreateNetworkAclEntry(ctx, input)
	return err
}

func (r *realNetworkACLAPI) DeleteEntry(ctx context.Context, networkAclId string, ruleNumber int, egress bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteNetworkAclEntry(ctx, &ec2sdk.DeleteNetworkAclEntryInput{
		NetworkAclId: aws.String(networkAclId),
		RuleNumber:   aws.Int32(int32(ruleNumber)), //nolint:gosec // G115: NACL rule numbers are 1-32766, within int32 range
		Egress:       aws.Bool(egress),
	})
	return err
}

func (r *realNetworkACLAPI) ReplaceEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input, err := replaceNetworkACLEntryInput(networkAclId, rule, egress)
	if err != nil {
		return err
	}
	_, err = r.client.ReplaceNetworkAclEntry(ctx, input)
	return err
}

func (r *realNetworkACLAPI) ReplaceNetworkACLAssociation(ctx context.Context, associationId string, networkAclId string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.ReplaceNetworkAclAssociation(ctx, &ec2sdk.ReplaceNetworkAclAssociationInput{
		AssociationId: aws.String(associationId),
		NetworkAclId:  aws.String(networkAclId),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.NewAssociationId), nil
}

func (r *realNetworkACLAPI) UpdateTags(ctx context.Context, networkAclId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}

	out, err := r.client.DescribeNetworkAcls(ctx, &ec2sdk.DescribeNetworkAclsInput{
		NetworkAclIds: []string{networkAclId},
	})
	if err != nil {
		return err
	}
	if len(out.NetworkAcls) > 0 {
		var oldTags []ec2types.Tag
		for _, tag := range out.NetworkAcls[0].Tags {
			key := aws.ToString(tag.Key)
			if strings.HasPrefix(key, "praxis:") {
				continue
			}
			oldTags = append(oldTags, ec2types.Tag{Key: tag.Key})
		}
		if len(oldTags) > 0 {
			if err := r.limiter.Wait(ctx); err != nil {
				return err
			}
			_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
				Resources: []string{networkAclId},
				Tags:      oldTags,
			})
			if err != nil {
				return err
			}
		}
	}

	var ec2Tags []ec2types.Tag
	for key, value := range tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	if len(ec2Tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
		Resources: []string{networkAclId},
		Tags:      ec2Tags,
	})
	return err
}

func (r *realNetworkACLAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeNetworkAcls(ctx, &ec2sdk.DescribeNetworkAclsInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, acl := range out.NetworkAcls {
		if id := aws.ToString(acl.NetworkAclId); id != "" {
			matches = append(matches, id)
		}
	}
	return singleManagedKeyMatch(managedKey, matches)
}

func (r *realNetworkACLAPI) FindAssociationIdForSubnet(ctx context.Context, subnetId string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeNetworkAcls(ctx, &ec2sdk.DescribeNetworkAclsInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("association.subnet-id"),
			Values: []string{subnetId},
		}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, acl := range out.NetworkAcls {
		for _, association := range acl.Associations {
			if aws.ToString(association.SubnetId) == subnetId {
				if id := aws.ToString(association.NetworkAclAssociationId); id != "" {
					matches = append(matches, id)
				}
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no network ACL association found for subnet %s", subnetId)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d network ACL associations found for subnet %s: %v", len(matches), subnetId, matches)
	}
}

func (r *realNetworkACLAPI) FindDefaultNetworkACL(ctx context.Context, vpcId string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeNetworkAcls(ctx, &ec2sdk.DescribeNetworkAclsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcId}},
			{Name: aws.String("default"), Values: []string{"true"}},
		},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, acl := range out.NetworkAcls {
		if aws.ToBool(acl.IsDefault) {
			if id := aws.ToString(acl.NetworkAclId); id != "" {
				matches = append(matches, id)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("default network ACL not found for VPC %s", vpcId)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d default network ACLs found for VPC %s: %v", len(matches), vpcId, matches)
	}
}

func networkACLEntryInput(networkAclId string, rule NetworkACLRule, egress bool) (*ec2sdk.CreateNetworkAclEntryInput, error) {
	protocol, err := normalizeProtocol(rule.Protocol)
	if err != nil {
		return nil, err
	}
	input := &ec2sdk.CreateNetworkAclEntryInput{
		NetworkAclId: aws.String(networkAclId),
		RuleNumber:   aws.Int32(int32(rule.RuleNumber)), //nolint:gosec // G115: NACL rule numbers are 1-32766, within int32 range
		Protocol:     aws.String(protocol),
		RuleAction:   ec2types.RuleAction(strings.ToLower(rule.RuleAction)),
		Egress:       aws.Bool(egress),
		CidrBlock:    aws.String(rule.CidrBlock),
	}
	applyRulePorts(input, protocol, rule)
	return input, nil
}

func replaceNetworkACLEntryInput(networkAclId string, rule NetworkACLRule, egress bool) (*ec2sdk.ReplaceNetworkAclEntryInput, error) {
	protocol, err := normalizeProtocol(rule.Protocol)
	if err != nil {
		return nil, err
	}
	input := &ec2sdk.ReplaceNetworkAclEntryInput{
		NetworkAclId: aws.String(networkAclId),
		RuleNumber:   aws.Int32(int32(rule.RuleNumber)), //nolint:gosec // G115: NACL rule numbers are 1-32766, within int32 range
		Protocol:     aws.String(protocol),
		RuleAction:   ec2types.RuleAction(strings.ToLower(rule.RuleAction)),
		Egress:       aws.Bool(egress),
		CidrBlock:    aws.String(rule.CidrBlock),
	}
	applyRulePorts(input, protocol, rule)
	return input, nil
}

func applyRulePorts(target any, protocol string, rule NetworkACLRule) {
	switch input := target.(type) {
	case *ec2sdk.CreateNetworkAclEntryInput:
		if protocol == "1" {
			input.IcmpTypeCode = &ec2types.IcmpTypeCode{Type: aws.Int32(int32(rule.FromPort)), Code: aws.Int32(int32(rule.ToPort))} //nolint:gosec // G115: NACL rule ports are bounded to valid range
			return
		}
		if protocol != "-1" {
			input.PortRange = &ec2types.PortRange{From: aws.Int32(int32(rule.FromPort)), To: aws.Int32(int32(rule.ToPort))} //nolint:gosec // G115: NACL rule ports are bounded to valid range
		}
	case *ec2sdk.ReplaceNetworkAclEntryInput:
		if protocol == "1" {
			input.IcmpTypeCode = &ec2types.IcmpTypeCode{Type: aws.Int32(int32(rule.FromPort)), Code: aws.Int32(int32(rule.ToPort))} //nolint:gosec // G115: NACL rule ports are bounded to valid range
			return
		}
		if protocol != "-1" {
			input.PortRange = &ec2types.PortRange{From: aws.Int32(int32(rule.FromPort)), To: aws.Int32(int32(rule.ToPort))} //nolint:gosec // G115: NACL rule ports are bounded to valid range
		}
	}
}

func networkACLRuleFromEntry(entry ec2types.NetworkAclEntry) (NetworkACLRule, bool) {
	ruleNumber := int(aws.ToInt32(entry.RuleNumber))
	if ruleNumber == 32767 {
		return NetworkACLRule{}, false
	}
	if aws.ToString(entry.Ipv6CidrBlock) != "" {
		return NetworkACLRule{}, false
	}
	rule := NetworkACLRule{
		RuleNumber: ruleNumber,
		Protocol:   aws.ToString(entry.Protocol),
		RuleAction: strings.ToLower(string(entry.RuleAction)),
		CidrBlock:  aws.ToString(entry.CidrBlock),
	}
	protocol, err := normalizeProtocol(rule.Protocol)
	if err == nil {
		rule.Protocol = protocol
	}
	if protocol == "1" {
		if entry.IcmpTypeCode != nil {
			rule.FromPort = int(aws.ToInt32(entry.IcmpTypeCode.Type))
			rule.ToPort = int(aws.ToInt32(entry.IcmpTypeCode.Code))
		}
		return rule, true
	}
	if entry.PortRange != nil {
		rule.FromPort = int(aws.ToInt32(entry.PortRange.From))
		rule.ToPort = int(aws.ToInt32(entry.PortRange.To))
	}
	return rule, true
}

func normalizeProtocol(value string) (string, error) {
	v := strings.TrimSpace(strings.ToLower(value))
	switch v {
	case "", "all", "-1":
		return "-1", nil
	case "tcp":
		return "6", nil
	case "udp":
		return "17", nil
	case "icmp":
		return "1", nil
	}
	number, err := strconv.Atoi(v)
	if err != nil {
		return "", fmt.Errorf("invalid protocol %q", value)
	}
	if number < -1 || number > 255 {
		return "", fmt.Errorf("invalid protocol %q", value)
	}
	return strconv.Itoa(number), nil
}

func sortRules(rules []NetworkACLRule) {
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].RuleNumber < rules[j].RuleNumber
	})
}

func sortAssociations(associations []NetworkACLAssociation) {
	sort.Slice(associations, func(i, j int) bool {
		return associations[i].SubnetId < associations[j].SubnetId
	})
}

func singleManagedKeyMatch(managedKey string, matches []string) (string, error) {
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d network ACLs claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

// Error classifiers — used by the driver to decide between retryable
// errors, terminal errors, and idempotent success paths.

// IsNotFound returns true when the NACL does not exist in AWS.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidNetworkAclID.NotFound")
}

// IsInUse returns true when the NACL cannot be deleted because subnets
// are still associated with it.
func IsInUse(err error) bool {
	return awserr.HasCode(err, "DependencyViolation")
}

// IsDefaultACL returns true when attempting to delete the VPC's default NACL,
// which AWS does not permit.
func IsDefaultACL(err error) bool {
	return awserr.HasCode(err, "Client.CannotDelete", "OperationNotPermitted")
}

// IsDuplicateRule returns true when a rule with the same number already exists.
// Used for idempotent rule creation.
func IsDuplicateRule(err error) bool {
	return awserr.HasCode(err, "NetworkAclEntryAlreadyExists")
}

// IsRuleNotFound returns true when trying to delete a rule that does not exist.
// Treated as a no-op.
func IsRuleNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidNetworkAclEntry.NotFound")
}

// IsInvalidParam returns true for invalid parameter errors (terminal).
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination", "InvalidSubnetID.NotFound", "InvalidVpcID.NotFound")
}

// IsLimitExceeded returns true when NACL or rule count limits are reached (terminal).
func IsLimitExceeded(err error) bool {
	return awserr.HasCodePrefix(err, "NetworkAclLimit", "RulesPerAclLimit")
}
