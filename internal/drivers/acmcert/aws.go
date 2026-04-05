// Package acmcert – aws.go
//
// This file contains the AWS API abstraction layer for AWS ACM Certificate.
// It defines the ACMCertificateAPI interface (used for testing with mocks)
// and the real implementation that calls AWS Certificate Manager (ACM) through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package acmcert

import (
	"context"
	"fmt"
	"github.com/shirvan/praxis/internal/drivers"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	acmsdk "github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// CertificateAPI abstracts all AWS Certificate Manager (ACM) SDK operations needed
// to manage a AWS ACM Certificate. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type CertificateAPI interface {
	RequestCertificate(ctx context.Context, spec ACMCertificateSpec) (string, error)
	DescribeCertificate(ctx context.Context, certificateArn string) (ObservedState, error)
	UpdateCertificateOptions(ctx context.Context, certificateArn string, options *CertificateOptions) error
	UpdateTags(ctx context.Context, certificateArn string, tags map[string]string) error
	DeleteCertificate(ctx context.Context, certificateArn string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

type realCertificateAPI struct {
	client  *acmsdk.Client
	limiter *ratelimit.Limiter
}

// NewCertificateAPI constructs a production ACMCertificateAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewCertificateAPI(client *acmsdk.Client) CertificateAPI {
	return &realCertificateAPI{client: client, limiter: ratelimit.New("acm-certificate", 10, 5)}
}

func (r *realCertificateAPI) RequestCertificate(ctx context.Context, spec ACMCertificateSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &acmsdk.RequestCertificateInput{
		DomainName:       aws.String(spec.DomainName),
		ValidationMethod: acmtypes.ValidationMethod(spec.ValidationMethod),
		Tags:             toSDKTags(withManagedKey(spec.ManagedKey, spec.Tags)),
	}
	if len(spec.SubjectAlternativeNames) > 0 {
		input.SubjectAlternativeNames = append([]string(nil), spec.SubjectAlternativeNames...)
	}
	if spec.KeyAlgorithm != "" {
		input.KeyAlgorithm = acmtypes.KeyAlgorithm(spec.KeyAlgorithm)
	}
	if spec.CertificateAuthorityArn != "" {
		input.CertificateAuthorityArn = aws.String(spec.CertificateAuthorityArn)
	}
	if spec.Options != nil {
		input.Options = &acmtypes.CertificateOptions{
			CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreference(normalizeTransparencyPreference(spec.Options)),
		}
	}
	if token := idempotencyToken(spec.ManagedKey); token != "" {
		input.IdempotencyToken = aws.String(token)
	}
	out, err := r.client.RequestCertificate(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.CertificateArn), nil
}

// DescribeCertificate reads the current state of the AWS ACM Certificate from AWS Certificate Manager (ACM).
func (r *realCertificateAPI) DescribeCertificate(ctx context.Context, certificateArn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeCertificate(ctx, &acmsdk.DescribeCertificateInput{CertificateArn: aws.String(certificateArn)})
	if err != nil {
		return ObservedState{}, err
	}
	if out.Certificate == nil {
		return ObservedState{}, fmt.Errorf("certificate %s returned empty details", certificateArn)
	}
	tags, err := r.listTags(ctx, certificateArn)
	if err != nil {
		return ObservedState{}, err
	}
	return fromCertificateDetail(out.Certificate, tags), nil
}

// UpdateCertificateOptions updates mutable properties of the AWS ACM Certificate via AWS Certificate Manager (ACM).
func (r *realCertificateAPI) UpdateCertificateOptions(ctx context.Context, certificateArn string, options *CertificateOptions) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateCertificateOptions(ctx, &acmsdk.UpdateCertificateOptionsInput{
		CertificateArn: aws.String(certificateArn),
		Options: &acmtypes.CertificateOptions{
			CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreference(normalizeTransparencyPreference(options)),
		},
	})
	return err
}

// UpdateTags updates mutable properties of the AWS ACM Certificate via AWS Certificate Manager (ACM).
func (r *realCertificateAPI) UpdateTags(ctx context.Context, certificateArn string, tags map[string]string) error {
	current, err := r.listTags(ctx, certificateArn)
	if err != nil {
		return err
	}
	desired := drivers.FilterPraxisTags(tags)
	removeTags := make([]acmtypes.Tag, 0)
	for key := range current {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if _, ok := desired[key]; !ok {
			removeTags = append(removeTags, acmtypes.Tag{Key: aws.String(key)})
		}
	}
	if len(removeTags) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, err := r.client.RemoveTagsFromCertificate(ctx, &acmsdk.RemoveTagsFromCertificateInput{
			CertificateArn: aws.String(certificateArn),
			Tags:           removeTags,
		}); err != nil {
			return err
		}
	}
	addTags := make([]acmtypes.Tag, 0, len(desired))
	for key, value := range desired {
		if currentValue, ok := current[key]; !ok || currentValue != value {
			addTags = append(addTags, acmtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
	}
	if len(addTags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.AddTagsToCertificate(ctx, &acmsdk.AddTagsToCertificateInput{
		CertificateArn: aws.String(certificateArn),
		Tags:           addTags,
	})
	return err
}

// DeleteCertificate removes the AWS ACM Certificate from AWS via AWS Certificate Manager (ACM).
func (r *realCertificateAPI) DeleteCertificate(ctx context.Context, certificateArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteCertificate(ctx, &acmsdk.DeleteCertificateInput{CertificateArn: aws.String(certificateArn)})
	return err
}

// FindByManagedKey searches for the AWS ACM Certificate using alternative identifiers.
func (r *realCertificateAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	paginator := acmsdk.NewListCertificatesPaginator(r.client, &acmsdk.ListCertificatesInput{})
	var matches []string
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return "", err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return "", err
		}
		for i := range page.CertificateSummaryList {
			summary := &page.CertificateSummaryList[i]
			certificateArn := aws.ToString(summary.CertificateArn)
			if certificateArn == "" {
				continue
			}
			tags, err := r.listTags(ctx, certificateArn)
			if err != nil {
				if IsNotFound(err) {
					continue
				}
				return "", err
			}
			if tags["praxis:managed-key"] == managedKey {
				matches = append(matches, certificateArn)
			}
		}
	}
	return singleManagedKeyMatch(managedKey, matches)
}

func (r *realCertificateAPI) listTags(ctx context.Context, certificateArn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForCertificate(ctx, &acmsdk.ListTagsForCertificateInput{CertificateArn: aws.String(certificateArn)})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

func fromCertificateDetail(detail *acmtypes.CertificateDetail, tags map[string]string) ObservedState {
	observed := ObservedState{
		CertificateArn:          aws.ToString(detail.CertificateArn),
		DomainName:              aws.ToString(detail.DomainName),
		SubjectAlternativeNames: append([]string(nil), detail.SubjectAlternativeNames...),
		KeyAlgorithm:            string(detail.KeyAlgorithm),
		CertificateAuthorityArn: aws.ToString(detail.CertificateAuthorityArn),
		Status:                  string(detail.Status),
		Tags:                    tags,
	}
	if len(detail.DomainValidationOptions) > 0 {
		observed.ValidationMethod = string(detail.DomainValidationOptions[0].ValidationMethod)
	}
	if detail.Options != nil {
		observed.Options = CertificateOptions{CertificateTransparencyLoggingPreference: string(detail.Options.CertificateTransparencyLoggingPreference)}
	}
	if detail.NotBefore != nil {
		observed.NotBefore = detail.NotBefore.UTC().Format(time.RFC3339)
	}
	if detail.NotAfter != nil {
		observed.NotAfter = detail.NotAfter.UTC().Format(time.RFC3339)
	}
	for _, validation := range detail.DomainValidationOptions {
		if validation.ResourceRecord == nil {
			continue
		}
		observed.DNSValidationRecords = append(observed.DNSValidationRecords, DNSValidationRecord{
			DomainName:          aws.ToString(validation.DomainName),
			ResourceRecordName:  aws.ToString(validation.ResourceRecord.Name),
			ResourceRecordType:  string(validation.ResourceRecord.Type),
			ResourceRecordValue: aws.ToString(validation.ResourceRecord.Value),
		})
	}
	slices.SortFunc(observed.SubjectAlternativeNames, strings.Compare)
	slices.SortFunc(observed.DNSValidationRecords, func(a, b DNSValidationRecord) int {
		return strings.Compare(a.DomainName, b.DomainName)
	})
	return normalizeObservedState(observed)
}

func toSDKTags(tags map[string]string) []acmtypes.Tag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]acmtypes.Tag, 0, len(tags))
	for key, value := range tags {
		out = append(out, acmtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	slices.SortFunc(out, func(a, b acmtypes.Tag) int {
		return strings.Compare(aws.ToString(a.Key), aws.ToString(b.Key))
	})
	return out
}

func withManagedKey(managedKey string, tags map[string]string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if managedKey != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func singleManagedKeyMatch(managedKey string, matches []string) (string, error) {
	slices.Sort(matches)
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d ACM certificates claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

func idempotencyToken(managedKey string) string {
	if managedKey == "" {
		return ""
	}
	b := strings.Builder{}
	for _, r := range managedKey {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() == 32 {
			break
		}
	}
	return strings.ToLower(b.String())
}

// IsNotFound returns true if the AWS error indicates the AWS ACM Certificate does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

func IsInvalidArn(err error) bool {
	return awserr.HasCode(err, "InvalidArnException")
}

func IsInvalidDomain(err error) bool {
	return awserr.HasCode(err, "InvalidDomainValidationOptionsException")
}

func IsInvalidState(err error) bool {
	return awserr.HasCode(err, "InvalidStateException")
}

func IsQuotaExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceededException")
}

func IsRequestInProgress(err error) bool {
	return awserr.HasCode(err, "RequestInProgressException")
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceInUseException")
}
