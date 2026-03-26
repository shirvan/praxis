package acmcert

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ACMCertificate"

type CertificateOptions struct {
	CertificateTransparencyLoggingPreference string `json:"certificateTransparencyLoggingPreference,omitempty"`
}

type DNSValidationRecord struct {
	DomainName          string `json:"domainName"`
	ResourceRecordName  string `json:"resourceRecordName"`
	ResourceRecordType  string `json:"resourceRecordType"`
	ResourceRecordValue string `json:"resourceRecordValue"`
}

type ACMCertificateSpec struct {
	Account                 string              `json:"account,omitempty"`
	Region                  string              `json:"region"`
	DomainName              string              `json:"domainName"`
	SubjectAlternativeNames []string            `json:"subjectAlternativeNames,omitempty"`
	ValidationMethod        string              `json:"validationMethod,omitempty"`
	KeyAlgorithm            string              `json:"keyAlgorithm,omitempty"`
	CertificateAuthorityArn string              `json:"certificateAuthorityArn,omitempty"`
	Options                 *CertificateOptions `json:"options,omitempty"`
	Tags                    map[string]string   `json:"tags,omitempty"`
	ManagedKey              string              `json:"managedKey,omitempty"`
}

type ACMCertificateOutputs struct {
	CertificateArn       string                `json:"certificateArn"`
	DomainName           string                `json:"domainName"`
	Status               string                `json:"status"`
	DNSValidationRecords []DNSValidationRecord `json:"dnsValidationRecords,omitempty"`
	NotBefore            string                `json:"notBefore,omitempty"`
	NotAfter             string                `json:"notAfter,omitempty"`
}

type ObservedState struct {
	CertificateArn          string                `json:"certificateArn"`
	DomainName              string                `json:"domainName"`
	SubjectAlternativeNames []string              `json:"subjectAlternativeNames,omitempty"`
	ValidationMethod        string                `json:"validationMethod,omitempty"`
	KeyAlgorithm            string                `json:"keyAlgorithm,omitempty"`
	CertificateAuthorityArn string                `json:"certificateAuthorityArn,omitempty"`
	Status                  string                `json:"status"`
	Options                 CertificateOptions    `json:"options"`
	Tags                    map[string]string     `json:"tags,omitempty"`
	DNSValidationRecords    []DNSValidationRecord `json:"dnsValidationRecords,omitempty"`
	NotBefore               string                `json:"notBefore,omitempty"`
	NotAfter                string                `json:"notAfter,omitempty"`
}

type ACMCertificateState struct {
	Desired            ACMCertificateSpec    `json:"desired"`
	Observed           ObservedState         `json:"observed"`
	Outputs            ACMCertificateOutputs `json:"outputs"`
	Status             types.ResourceStatus  `json:"status"`
	Mode               types.Mode            `json:"mode"`
	Error              string                `json:"error,omitempty"`
	Generation         int64                 `json:"generation"`
	LastReconcile      string                `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
