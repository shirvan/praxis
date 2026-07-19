package nacl

import (
	"fmt"
	"sort"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/drivers"
)

// applyDesiredState converges rules, associations, and tags to match the
// desired spec. Delegates to applyRuleDiff for ingress/egress rules and
// applyAssociationDiff for subnet associations.
func (o *genericOperations) applyDesiredState(ctx restate.ObjectContext, api NetworkACLAPI, networkAclID string, desired NetworkACLSpec, observed ObservedState) error {
	if err := o.applyRuleDiff(ctx, api, networkAclID, desired.IngressRules, observed.IngressRules, false); err != nil {
		return err
	}
	if err := o.applyRuleDiff(ctx, api, networkAclID, desired.EgressRules, observed.EgressRules, true); err != nil {
		return err
	}
	if err := o.applyAssociationDiff(ctx, api, networkAclID, desired.VpcId, desired.SubnetAssociations, observed.Associations); err != nil {
		return err
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, networkAclID, desired.Tags)
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

// applyRuleDiff computes the diff between desired and observed rules by
// rule number, then applies changes in add → replace → remove order.
func (o *genericOperations) applyRuleDiff(ctx restate.ObjectContext, api NetworkACLAPI, networkAclID string, desiredRules, observedRules []NetworkACLRule, egress bool) error {
	desiredMap := ruleMap(desiredRules)
	observedMap := ruleMap(observedRules)

	var toAdd []NetworkACLRule
	var toReplace []NetworkACLRule
	var toRemove []NetworkACLRule

	for ruleNumber, desiredRule := range desiredMap {
		observedRule, ok := observedMap[ruleNumber]
		if !ok {
			toAdd = append(toAdd, desiredRule)
			continue
		}
		if !ruleEqual(desiredRule, observedRule) {
			toReplace = append(toReplace, desiredRule)
		}
	}
	for ruleNumber, observedRule := range observedMap {
		if _, ok := desiredMap[ruleNumber]; !ok {
			toRemove = append(toRemove, observedRule)
		}
	}

	sortRules(toAdd)
	sortRules(toReplace)
	sortRules(toRemove)

	for _, rule := range toAdd {
		_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.CreateEntry(rc, networkAclID, rule, egress)
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("create rule %d: %w", rule.RuleNumber, err)
		}
	}

	for _, rule := range toReplace {
		_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ReplaceEntry(rc, networkAclID, rule, egress)
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("replace rule %d: %w", rule.RuleNumber, err)
		}
	}

	for _, rule := range toRemove {
		_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteEntry(rc, networkAclID, rule.RuleNumber, egress)
			if IsRuleNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("delete rule %d: %w", rule.RuleNumber, err)
		}
	}

	return nil
}

// applyAssociationDiff adds and removes subnet associations to match the
// desired set. Adding associates the subnet with this NACL; removing
// reassociates the subnet to the VPC's default NACL.
func (o *genericOperations) applyAssociationDiff(ctx restate.ObjectContext, api NetworkACLAPI, networkAclID string, vpcID string, desiredSubnets []string, observedAssociations []NetworkACLAssociation) error {
	desiredSet := make(map[string]struct{}, len(desiredSubnets))
	for _, subnetID := range desiredSubnets {
		desiredSet[subnetID] = struct{}{}
	}
	observedBySubnet := make(map[string]NetworkACLAssociation, len(observedAssociations))
	for _, association := range observedAssociations {
		observedBySubnet[association.SubnetId] = association
	}

	for _, subnetID := range desiredSubnets {
		if _, ok := observedBySubnet[subnetID]; ok {
			continue
		}
		associationID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindAssociationIdForSubnet(rc, subnetID)
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("find network ACL association for subnet %s: %w", subnetID, err)
		}
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.ReplaceNetworkACLAssociation(rc, associationID, networkAclID)
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("associate subnet %s: %w", subnetID, err)
		}
	}

	var toRemove []NetworkACLAssociation
	for _, association := range observedAssociations {
		if _, ok := desiredSet[association.SubnetId]; !ok {
			toRemove = append(toRemove, association)
		}
	}
	if len(toRemove) == 0 {
		return nil
	}

	defaultACLID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.FindDefaultNetworkACL(rc, vpcID)
	}, classifyMutation)
	if err != nil {
		return fmt.Errorf("find default network ACL for VPC %s: %w", vpcID, err)
	}

	for _, association := range toRemove {
		associationID := association.AssociationId
		if associationID == "" {
			associationID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
				return api.FindAssociationIdForSubnet(rc, association.SubnetId)
			}, classifyMutation)
			if err != nil {
				return fmt.Errorf("find network ACL association for subnet %s: %w", association.SubnetId, err)
			}
		}
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.ReplaceNetworkACLAssociation(rc, associationID, defaultACLID)
		}, classifyMutation)
		if err != nil {
			return fmt.Errorf("reassociate subnet %s to default network ACL: %w", association.SubnetId, err)
		}
	}

	return nil
}

// normalizeSpec validates and normalizes a NetworkACLSpec: checks required
// fields, normalizes protocol names to IANA numbers, validates rule number
// ranges (1-32766), deduplicates rules and subnet associations.
func normalizeSpec(spec NetworkACLSpec) (NetworkACLSpec, error) {
	if spec.Region == "" {
		return NetworkACLSpec{}, fmt.Errorf("region is required")
	}
	if spec.VpcId == "" {
		return NetworkACLSpec{}, fmt.Errorf("vpcId is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	normalizedIngress, err := normalizeRuleSet(spec.IngressRules, "ingressRules")
	if err != nil {
		return NetworkACLSpec{}, err
	}
	normalizedEgress, err := normalizeRuleSet(spec.EgressRules, "egressRules")
	if err != nil {
		return NetworkACLSpec{}, err
	}
	cleanSubnets := make([]string, 0, len(spec.SubnetAssociations))
	seenSubnets := make(map[string]struct{}, len(spec.SubnetAssociations))
	for _, subnetID := range spec.SubnetAssociations {
		subnetID = strings.TrimSpace(subnetID)
		if subnetID == "" {
			return NetworkACLSpec{}, fmt.Errorf("subnetAssociations cannot contain empty values")
		}
		if _, ok := seenSubnets[subnetID]; ok {
			return NetworkACLSpec{}, fmt.Errorf("subnetAssociations contains duplicate subnet %q", subnetID)
		}
		seenSubnets[subnetID] = struct{}{}
		cleanSubnets = append(cleanSubnets, subnetID)
	}
	sort.Strings(cleanSubnets)
	spec.IngressRules = normalizedIngress
	spec.EgressRules = normalizedEgress
	spec.SubnetAssociations = cleanSubnets
	return spec, nil
}

func normalizeRuleSet(rules []NetworkACLRule, label string) ([]NetworkACLRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	normalized := make([]NetworkACLRule, 0, len(rules))
	seen := make(map[int]struct{}, len(rules))
	for _, rule := range rules {
		rule, err := normalizeAndValidateRule(rule)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if _, ok := seen[rule.RuleNumber]; ok {
			return nil, fmt.Errorf("%s contains duplicate ruleNumber %d", label, rule.RuleNumber)
		}
		seen[rule.RuleNumber] = struct{}{}
		normalized = append(normalized, rule)
	}
	sortRules(normalized)
	return normalized, nil
}

func normalizeAndValidateRule(rule NetworkACLRule) (NetworkACLRule, error) {
	if rule.RuleNumber < 1 || rule.RuleNumber > 32766 {
		return NetworkACLRule{}, fmt.Errorf("ruleNumber %d must be between 1 and 32766", rule.RuleNumber)
	}
	protocol, err := normalizeProtocol(rule.Protocol)
	if err != nil {
		return NetworkACLRule{}, err
	}
	rule.Protocol = protocol
	rule.RuleAction = strings.ToLower(strings.TrimSpace(rule.RuleAction))
	if rule.RuleAction != "allow" && rule.RuleAction != "deny" {
		return NetworkACLRule{}, fmt.Errorf("rule %d has invalid ruleAction %q", rule.RuleNumber, rule.RuleAction)
	}
	rule.CidrBlock = strings.TrimSpace(rule.CidrBlock)
	if rule.CidrBlock == "" {
		return NetworkACLRule{}, fmt.Errorf("rule %d requires cidrBlock", rule.RuleNumber)
	}
	switch protocol {
	case "-1":
		if rule.FromPort != 0 || rule.ToPort != 0 {
			return NetworkACLRule{}, fmt.Errorf("rule %d with protocol -1 must use fromPort=0 and toPort=0", rule.RuleNumber)
		}
	case "1":
		if rule.FromPort < -1 || rule.FromPort > 255 || rule.ToPort < -1 || rule.ToPort > 255 {
			return NetworkACLRule{}, fmt.Errorf("rule %d ICMP type/code must be between -1 and 255", rule.RuleNumber)
		}
	default:
		if rule.FromPort < 0 || rule.FromPort > 65535 || rule.ToPort < 0 || rule.ToPort > 65535 {
			return NetworkACLRule{}, fmt.Errorf("rule %d ports must be between 0 and 65535", rule.RuleNumber)
		}
		if rule.ToPort < rule.FromPort {
			return NetworkACLRule{}, fmt.Errorf("rule %d toPort must be >= fromPort", rule.RuleNumber)
		}
	}
	return rule, nil
}

func specFromObserved(obs ObservedState) NetworkACLSpec {
	return NetworkACLSpec{
		VpcId:              obs.VpcId,
		IngressRules:       append([]NetworkACLRule(nil), obs.IngressRules...),
		EgressRules:        append([]NetworkACLRule(nil), obs.EgressRules...),
		SubnetAssociations: subnetIDsFromAssociations(obs.Associations),
		Tags:               drivers.FilterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) NetworkACLOutputs {
	ingress := append([]NetworkACLRule(nil), obs.IngressRules...)
	egress := append([]NetworkACLRule(nil), obs.EgressRules...)
	associations := append([]NetworkACLAssociation(nil), obs.Associations...)
	sortRules(ingress)
	sortRules(egress)
	sortAssociations(associations)
	return NetworkACLOutputs{
		NetworkAclId: obs.NetworkAclId,
		VpcId:        obs.VpcId,
		IsDefault:    obs.IsDefault,
		IngressRules: ingress,
		EgressRules:  egress,
		Associations: associations,
	}
}

func subnetIDsFromAssociations(associations []NetworkACLAssociation) []string {
	ids := make([]string, 0, len(associations))
	for _, association := range associations {
		if association.SubnetId != "" {
			ids = append(ids, association.SubnetId)
		}
	}
	sort.Strings(ids)
	return ids
}

func formatManagedKeyConflict(managedKey, networkAclID string) error {
	return fmt.Errorf("network ACL name %q in this VPC is already managed by Praxis (networkAclId: %s); remove the existing resource or use a different metadata.name", managedKey, networkAclID)
}
