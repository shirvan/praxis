package acmcert

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

func specFromObserved(observed ObservedState) ACMCertificateSpec {
	options := &CertificateOptions{CertificateTransparencyLoggingPreference: normalizeTransparencyPreference(&observed.Options)}
	return ACMCertificateSpec{
		DomainName: observed.DomainName, SubjectAlternativeNames: append([]string(nil), observed.SubjectAlternativeNames...),
		ValidationMethod: observed.ValidationMethod, KeyAlgorithm: observed.KeyAlgorithm,
		CertificateAuthorityArn: observed.CertificateAuthorityArn, Options: options,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ACMCertificateOutputs {
	return ACMCertificateOutputs{
		CertificateArn: observed.CertificateArn, DomainName: observed.DomainName, Status: observed.Status,
		DNSValidationRecords: append([]DNSValidationRecord(nil), observed.DNSValidationRecords...),
		NotBefore:            observed.NotBefore, NotAfter: observed.NotAfter,
	}
}

func applyDefaults(spec ACMCertificateSpec) ACMCertificateSpec {
	spec.DomainName = normalizeDomainName(spec.DomainName)
	spec.SubjectAlternativeNames = normalizeSANs(spec.SubjectAlternativeNames)
	if spec.ValidationMethod == "" {
		spec.ValidationMethod = "DNS"
	} else {
		spec.ValidationMethod = strings.ToUpper(strings.TrimSpace(spec.ValidationMethod))
	}
	if spec.KeyAlgorithm == "" {
		spec.KeyAlgorithm = "RSA_2048"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Options == nil {
		spec.Options = &CertificateOptions{}
	}
	spec.Options.CertificateTransparencyLoggingPreference = normalizeTransparencyPreference(spec.Options)
	return spec
}

func normalizeObservedState(observed ObservedState) ObservedState {
	observed.DomainName = normalizeDomainName(observed.DomainName)
	observed.SubjectAlternativeNames = normalizeSANs(observed.SubjectAlternativeNames)
	observed.ValidationMethod = strings.ToUpper(strings.TrimSpace(observed.ValidationMethod))
	observed.KeyAlgorithm = strings.TrimSpace(observed.KeyAlgorithm)
	observed.Options.CertificateTransparencyLoggingPreference = normalizeTransparencyPreference(&observed.Options)
	if observed.Tags == nil {
		observed.Tags = map[string]string{}
	}
	for i := range observed.DNSValidationRecords {
		observed.DNSValidationRecords[i].DomainName = normalizeDomainName(observed.DNSValidationRecords[i].DomainName)
		observed.DNSValidationRecords[i].ResourceRecordName = strings.TrimSpace(observed.DNSValidationRecords[i].ResourceRecordName)
		observed.DNSValidationRecords[i].ResourceRecordType = strings.TrimSpace(observed.DNSValidationRecords[i].ResourceRecordType)
		observed.DNSValidationRecords[i].ResourceRecordValue = strings.TrimSpace(observed.DNSValidationRecords[i].ResourceRecordValue)
	}
	slices.SortFunc(observed.SubjectAlternativeNames, strings.Compare)
	slices.SortFunc(observed.DNSValidationRecords, func(a, b DNSValidationRecord) int {
		if a.DomainName == b.DomainName {
			return strings.Compare(a.ResourceRecordName, b.ResourceRecordName)
		}
		return strings.Compare(a.DomainName, b.DomainName)
	})
	return observed
}

func normalizeDomainName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// EquivalentSANs accounts for ACM always including the primary domain in the
// provider SAN set even when it was omitted from the request SAN list.
func EquivalentSANs(desiredDomain string, desiredSANs []string, observedDomain string, observedSANs []string) bool {
	want := normalizeSANs(append(append([]string(nil), desiredSANs...), desiredDomain))
	have := normalizeSANs(append(append([]string(nil), observedSANs...), observedDomain))
	return slices.Equal(want, have)
}

func normalizeSANs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeDomainName(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	slices.Sort(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeTransparencyPreference(options *CertificateOptions) string {
	if options == nil || strings.TrimSpace(options.CertificateTransparencyLoggingPreference) == "" {
		return "ENABLED"
	}
	return strings.ToUpper(strings.TrimSpace(options.CertificateTransparencyLoggingPreference))
}

func validateImmutableFields(desired ACMCertificateSpec, observed ObservedState) error {
	desired = applyDefaults(desired)
	observed = normalizeObservedState(observed)
	if desired.DomainName != observed.DomainName {
		return fmt.Errorf("certificate %s already exists with domainName %s; requested domainName %s cannot be changed", observed.CertificateArn, observed.DomainName, desired.DomainName)
	}
	if !EquivalentSANs(desired.DomainName, desired.SubjectAlternativeNames, observed.DomainName, observed.SubjectAlternativeNames) {
		return fmt.Errorf("certificate %s already exists with different subjectAlternativeNames; requested SANs cannot be changed in place", observed.CertificateArn)
	}
	if desired.ValidationMethod != "" && observed.ValidationMethod != "" && desired.ValidationMethod != observed.ValidationMethod {
		return fmt.Errorf("certificate %s already exists with validationMethod %s; requested validationMethod %s cannot be changed", observed.CertificateArn, observed.ValidationMethod, desired.ValidationMethod)
	}
	if desired.KeyAlgorithm != "" && observed.KeyAlgorithm != "" && desired.KeyAlgorithm != observed.KeyAlgorithm {
		return fmt.Errorf("certificate %s already exists with keyAlgorithm %s; requested keyAlgorithm %s cannot be changed", observed.CertificateArn, observed.KeyAlgorithm, desired.KeyAlgorithm)
	}
	if strings.TrimSpace(desired.CertificateAuthorityArn) != strings.TrimSpace(observed.CertificateAuthorityArn) {
		return fmt.Errorf("certificate %s already exists with certificateAuthorityArn %s; requested certificateAuthorityArn %s cannot be changed", observed.CertificateArn, observed.CertificateAuthorityArn, desired.CertificateAuthorityArn)
	}
	return nil
}
