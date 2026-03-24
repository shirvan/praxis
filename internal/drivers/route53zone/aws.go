package route53zone

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	route53sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	route53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type HostedZoneAPI interface {
	CreateHostedZone(ctx context.Context, spec HostedZoneSpec) (string, error)
	DescribeHostedZone(ctx context.Context, hostedZoneID string) (ObservedState, error)
	FindHostedZoneByName(ctx context.Context, name string) (string, error)
	UpdateComment(ctx context.Context, hostedZoneID, comment string) error
	AssociateVPC(ctx context.Context, hostedZoneID string, vpc HostedZoneVPC) error
	DisassociateVPC(ctx context.Context, hostedZoneID string, vpc HostedZoneVPC) error
	UpdateTags(ctx context.Context, hostedZoneID string, tags map[string]string) error
	DeleteHostedZone(ctx context.Context, hostedZoneID string) error
}

type realHostedZoneAPI struct {
	client  *route53sdk.Client
	limiter *ratelimit.Limiter
}

func NewHostedZoneAPI(client *route53sdk.Client) HostedZoneAPI {
	return &realHostedZoneAPI{client: client, limiter: ratelimit.New("route53", 5, 3)}
}

func (r *realHostedZoneAPI) CreateHostedZone(ctx context.Context, spec HostedZoneSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	callerReference := spec.ManagedKey
	if callerReference == "" {
		callerReference = spec.Name
	}
	input := &route53sdk.CreateHostedZoneInput{
		CallerReference: aws.String(callerReference),
		Name:            aws.String(spec.Name),
	}
	if spec.Comment != "" || spec.IsPrivate {
		input.HostedZoneConfig = &route53types.HostedZoneConfig{PrivateZone: spec.IsPrivate}
		if spec.Comment != "" {
			input.HostedZoneConfig.Comment = aws.String(spec.Comment)
		}
	}
	if spec.IsPrivate && len(spec.VPCs) > 0 {
		input.VPC = toRoute53VPC(spec.VPCs[0])
	}
	out, err := r.client.CreateHostedZone(ctx, input)
	if err != nil {
		return "", err
	}
	hostedZoneID := normalizeHostedZoneID(aws.ToString(out.HostedZone.Id))
	if err := r.UpdateTags(ctx, hostedZoneID, spec.Tags); err != nil {
		return "", err
	}
	if len(spec.VPCs) > 1 {
		for _, vpc := range spec.VPCs[1:] {
			if err := r.AssociateVPC(ctx, hostedZoneID, vpc); err != nil {
				return "", err
			}
		}
	}
	return hostedZoneID, nil
}

func (r *realHostedZoneAPI) DescribeHostedZone(ctx context.Context, hostedZoneID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetHostedZone(ctx, &route53sdk.GetHostedZoneInput{Id: aws.String(hostedZoneID)})
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.listTags(ctx, route53types.TagResourceTypeHostedzone, hostedZoneID)
	if err != nil {
		return ObservedState{}, err
	}
	observed := ObservedState{
		HostedZoneId:    normalizeHostedZoneID(aws.ToString(out.HostedZone.Id)),
		Name:            normalizeZoneName(aws.ToString(out.HostedZone.Name)),
		CallerReference: aws.ToString(out.HostedZone.CallerReference),
		Comment:         hostedZoneComment(out.HostedZone.Config),
		IsPrivate:       hostedZonePrivate(out.HostedZone.Config),
		VPCs:            fromRoute53VPCs(out.VPCs),
		Tags:            tags,
		NameServers:     normalizeStringSlice(out.DelegationSet.NameServers),
		RecordCount:     aws.ToInt64(out.HostedZone.ResourceRecordSetCount),
	}
	return normalizeObservedState(observed), nil
}

func (r *realHostedZoneAPI) FindHostedZoneByName(ctx context.Context, name string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	target := normalizeZoneName(name)
	out, err := r.client.ListHostedZonesByName(ctx, &route53sdk.ListHostedZonesByNameInput{DNSName: aws.String(target), MaxItems: aws.Int32(10)})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, zone := range out.HostedZones {
		if normalizeZoneName(aws.ToString(zone.Name)) == target {
			matches = append(matches, normalizeHostedZoneID(aws.ToString(zone.Id)))
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple hosted zones exist for domain %q: %v; manual intervention required", target, matches)
	}
}

func (r *realHostedZoneAPI) UpdateComment(ctx context.Context, hostedZoneID, comment string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &route53sdk.UpdateHostedZoneCommentInput{Id: aws.String(hostedZoneID)}
	if strings.TrimSpace(comment) != "" {
		input.Comment = aws.String(strings.TrimSpace(comment))
	}
	_, err := r.client.UpdateHostedZoneComment(ctx, input)
	return err
}

func (r *realHostedZoneAPI) AssociateVPC(ctx context.Context, hostedZoneID string, vpc HostedZoneVPC) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.AssociateVPCWithHostedZone(ctx, &route53sdk.AssociateVPCWithHostedZoneInput{
		HostedZoneId: aws.String(hostedZoneID),
		VPC:          toRoute53VPC(vpc),
	})
	if err != nil {
		return err
	}
	return r.waitForChange(ctx, aws.ToString(out.ChangeInfo.Id))
}

func (r *realHostedZoneAPI) DisassociateVPC(ctx context.Context, hostedZoneID string, vpc HostedZoneVPC) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.DisassociateVPCFromHostedZone(ctx, &route53sdk.DisassociateVPCFromHostedZoneInput{
		HostedZoneId: aws.String(hostedZoneID),
		VPC:          toRoute53VPC(vpc),
	})
	if err != nil {
		return err
	}
	return r.waitForChange(ctx, aws.ToString(out.ChangeInfo.Id))
}

func (r *realHostedZoneAPI) UpdateTags(ctx context.Context, hostedZoneID string, tags map[string]string) error {
	current, err := r.listTags(ctx, route53types.TagResourceTypeHostedzone, hostedZoneID)
	if err != nil {
		return err
	}
	addTags := make([]route53types.Tag, 0, len(tags))
	for key, value := range tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if currentValue, ok := current[key]; !ok || currentValue != value {
			addTags = append(addTags, route53types.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
	}
	removeKeys := make([]string, 0)
	for key := range current {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if _, ok := tags[key]; !ok {
			removeKeys = append(removeKeys, key)
		}
	}
	if len(addTags) == 0 && len(removeKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.ChangeTagsForResource(ctx, &route53sdk.ChangeTagsForResourceInput{
		ResourceId:    aws.String(hostedZoneID),
		ResourceType:  route53types.TagResourceTypeHostedzone,
		AddTags:       addTags,
		RemoveTagKeys: removeKeys,
	})
	return err
}

func (r *realHostedZoneAPI) DeleteHostedZone(ctx context.Context, hostedZoneID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteHostedZone(ctx, &route53sdk.DeleteHostedZoneInput{Id: aws.String(hostedZoneID)})
	return err
}

func (r *realHostedZoneAPI) listTags(ctx context.Context, resourceType route53types.TagResourceType, resourceID string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &route53sdk.ListTagsForResourceInput{
		ResourceId:   aws.String(resourceID),
		ResourceType: resourceType,
	})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.ResourceTagSet.Tags))
	for _, tag := range out.ResourceTagSet.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

func (r *realHostedZoneAPI) waitForChange(ctx context.Context, changeID string) error {
	if strings.TrimSpace(changeID) == "" {
		return nil
	}
	return route53sdk.NewResourceRecordSetsChangedWaiter(r.client).Wait(ctx, &route53sdk.GetChangeInput{Id: aws.String(changeID)}, 2*time.Minute)
}

func toRoute53VPC(vpc HostedZoneVPC) *route53types.VPC {
	return &route53types.VPC{VPCId: aws.String(strings.TrimSpace(vpc.VpcId)), VPCRegion: route53types.VPCRegion(strings.TrimSpace(vpc.VpcRegion))}
}

func fromRoute53VPCs(vpcs []route53types.VPC) []HostedZoneVPC {
	out := make([]HostedZoneVPC, 0, len(vpcs))
	for _, vpc := range vpcs {
		out = append(out, HostedZoneVPC{VpcId: aws.ToString(vpc.VPCId), VpcRegion: string(vpc.VPCRegion)})
	}
	return normalizeHostedZoneVPCs(out)
}

func hostedZoneComment(config *route53types.HostedZoneConfig) string {
	if config == nil {
		return ""
	}
	return aws.ToString(config.Comment)
}

func hostedZonePrivate(config *route53types.HostedZoneConfig) bool {
	return config != nil && config.PrivateZone
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "NoSuchHostedZone")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "HostedZoneAlreadyExists")
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "ConflictingDomainExists", "PriorRequestNotComplete")
}

func IsInvalidInput(err error) bool {
	return awserr.HasCode(err, "InvalidInput")
}

func IsNotEmpty(err error) bool {
	return awserr.HasCode(err, "HostedZoneNotEmpty")
}
