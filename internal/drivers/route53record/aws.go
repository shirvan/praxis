package route53record

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

// RecordAPI defines the interface for Route53 DNS record operations.
// Uses UPSERT for both create and update, with waitForChange for propagation.
type RecordAPI interface {
	UpsertRecord(ctx context.Context, spec RecordSpec) error
	DescribeRecord(ctx context.Context, identity RecordIdentity) (ObservedState, error)
	DeleteRecord(ctx context.Context, observed ObservedState) error
}

type realRecordAPI struct {
	client  *route53sdk.Client
	limiter *ratelimit.Limiter
}

// NewRecordAPI constructs a production RecordAPI with Route53 rate limiting.
func NewRecordAPI(client *route53sdk.Client) RecordAPI {
	return &realRecordAPI{client: client, limiter: ratelimit.New("route53", 5, 3)}
}

// UpsertRecord calls ChangeResourceRecordSets with UPSERT action, creating or updating the record.
func (r *realRecordAPI) UpsertRecord(ctx context.Context, spec RecordSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.ChangeResourceRecordSets(ctx, &route53sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(spec.HostedZoneId),
		ChangeBatch:  &route53types.ChangeBatch{Changes: []route53types.Change{{Action: route53types.ChangeActionUpsert, ResourceRecordSet: toRoute53RecordSet(spec)}}},
	})
	if err != nil {
		return err
	}
	return r.waitForChange(ctx, aws.ToString(out.ChangeInfo.Id))
}

func (r *realRecordAPI) DescribeRecord(ctx context.Context, identity RecordIdentity) (ObservedState, error) {
	paginator := route53sdk.NewListResourceRecordSetsPaginator(r.client, &route53sdk.ListResourceRecordSetsInput{HostedZoneId: aws.String(identity.HostedZoneId), MaxItems: aws.Int32(100)})
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return ObservedState{}, err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return ObservedState{}, err
		}
		for i := range page.ResourceRecordSets {
			candidate := fromRoute53RecordSet(identity.HostedZoneId, page.ResourceRecordSets[i])
			if candidate.Name == normalizeRecordName(identity.Name) && strings.EqualFold(candidate.Type, identity.Type) && candidate.SetIdentifier == strings.TrimSpace(identity.SetIdentifier) {
				return candidate, nil
			}
		}
	}
	return ObservedState{}, awserr.NotFound(fmt.Sprintf("record %s %s not found in hosted zone %s", identity.Name, identity.Type, identity.HostedZoneId))
}

func (r *realRecordAPI) DeleteRecord(ctx context.Context, observed ObservedState) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.ChangeResourceRecordSets(ctx, &route53sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(observed.HostedZoneId),
		ChangeBatch:  &route53types.ChangeBatch{Changes: []route53types.Change{{Action: route53types.ChangeActionDelete, ResourceRecordSet: toRoute53RecordSet(specFromObserved(observed))}}},
	})
	if err != nil {
		return err
	}
	return r.waitForChange(ctx, aws.ToString(out.ChangeInfo.Id))
}

func (r *realRecordAPI) waitForChange(ctx context.Context, changeID string) error {
	if strings.TrimSpace(changeID) == "" {
		return nil
	}
	return route53sdk.NewResourceRecordSetsChangedWaiter(r.client).Wait(ctx, &route53sdk.GetChangeInput{Id: aws.String(changeID)}, 2*time.Minute)
}

func toRoute53RecordSet(spec RecordSpec) *route53types.ResourceRecordSet {
	recordSet := &route53types.ResourceRecordSet{Name: aws.String(spec.Name), Type: route53types.RRType(spec.Type)}
	if spec.AliasTarget != nil {
		recordSet.AliasTarget = &route53types.AliasTarget{HostedZoneId: aws.String(spec.AliasTarget.HostedZoneId), DNSName: aws.String(spec.AliasTarget.DNSName), EvaluateTargetHealth: spec.AliasTarget.EvaluateTargetHealth}
	} else {
		recordSet.TTL = aws.Int64(spec.TTL)
		recordSet.ResourceRecords = make([]route53types.ResourceRecord, 0, len(spec.ResourceRecords))
		for _, value := range spec.ResourceRecords {
			recordSet.ResourceRecords = append(recordSet.ResourceRecords, route53types.ResourceRecord{Value: aws.String(value)})
		}
	}
	if spec.SetIdentifier != "" {
		recordSet.SetIdentifier = aws.String(spec.SetIdentifier)
	}
	if spec.Weight != 0 {
		recordSet.Weight = aws.Int64(spec.Weight)
	}
	if spec.Region != "" {
		recordSet.Region = route53types.ResourceRecordSetRegion(spec.Region)
	}
	if spec.Failover != "" {
		recordSet.Failover = route53types.ResourceRecordSetFailover(spec.Failover)
	}
	if spec.GeoLocation != nil {
		recordSet.GeoLocation = &route53types.GeoLocation{ContinentCode: aws.String(spec.GeoLocation.ContinentCode), CountryCode: aws.String(spec.GeoLocation.CountryCode), SubdivisionCode: aws.String(spec.GeoLocation.SubdivisionCode)}
	}
	if spec.MultiValueAnswer {
		recordSet.MultiValueAnswer = aws.Bool(true)
	}
	if spec.HealthCheckId != "" {
		recordSet.HealthCheckId = aws.String(spec.HealthCheckId)
	}
	return recordSet
}

func fromRoute53RecordSet(hostedZoneID string, recordSet route53types.ResourceRecordSet) ObservedState {
	observed := ObservedState{HostedZoneId: normalizeHostedZoneID(hostedZoneID), Name: normalizeRecordName(aws.ToString(recordSet.Name)), Type: string(recordSet.Type), TTL: aws.ToInt64(recordSet.TTL), SetIdentifier: aws.ToString(recordSet.SetIdentifier), Weight: aws.ToInt64(recordSet.Weight), Region: string(recordSet.Region), Failover: string(recordSet.Failover), MultiValueAnswer: aws.ToBool(recordSet.MultiValueAnswer), HealthCheckId: aws.ToString(recordSet.HealthCheckId)}
	if len(recordSet.ResourceRecords) > 0 {
		observed.ResourceRecords = make([]string, 0, len(recordSet.ResourceRecords))
		for _, record := range recordSet.ResourceRecords {
			observed.ResourceRecords = append(observed.ResourceRecords, aws.ToString(record.Value))
		}
	}
	if recordSet.AliasTarget != nil {
		observed.AliasTarget = &AliasTarget{HostedZoneId: normalizeHostedZoneID(aws.ToString(recordSet.AliasTarget.HostedZoneId)), DNSName: strings.TrimSuffix(aws.ToString(recordSet.AliasTarget.DNSName), "."), EvaluateTargetHealth: recordSet.AliasTarget.EvaluateTargetHealth}
	}
	if recordSet.GeoLocation != nil {
		observed.GeoLocation = &GeoLocation{ContinentCode: aws.ToString(recordSet.GeoLocation.ContinentCode), CountryCode: aws.ToString(recordSet.GeoLocation.CountryCode), SubdivisionCode: aws.ToString(recordSet.GeoLocation.SubdivisionCode)}
	}
	return normalizeObservedState(observed)
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidInput") || awserr.IsNotFoundErr(err)
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "PriorRequestNotComplete")
}

func IsInvalidInput(err error) bool {
	return awserr.HasCode(err, "InvalidInput", "InvalidChangeBatch")
}
