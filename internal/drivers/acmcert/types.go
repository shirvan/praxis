// Package acmcert implements the Praxis driver for AWS ACM Certificate resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// AWS Certificate Manager (ACM); the driver state couples both together with status tracking.
package acmcert

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ACM Certificate driver.
const ServiceName = "ACMCertificate"

// CertificateOptions controls optional ACM certificate settings like CT logging.
type CertificateOptions struct {
	CertificateTransparencyLoggingPreference string `json:"certificateTransparencyLoggingPreference,omitempty"`
}

// DNSValidationRecord holds the DNS CNAME record needed to validate domain ownership for an ACM certificate.
type DNSValidationRecord struct {
	DomainName          string `json:"domainName"`
	ResourceRecordName  string `json:"resourceRecordName"`
	ResourceRecordType  string `json:"resourceRecordType"`
	ResourceRecordValue string `json:"resourceRecordValue"`
}

// ACMCertificateSpec declares the user's desired configuration for a AWS ACM Certificate.
// Fields are validated before any AWS call and mapped to AWS Certificate Manager (ACM) API inputs.
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

// ACMCertificateOutputs holds the values produced after provisioning a AWS ACM Certificate.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type ACMCertificateOutputs struct {
	CertificateArn       string                `json:"certificateArn"`
	DomainName           string                `json:"domainName"`
	Status               string                `json:"status"`
	DNSValidationRecords []DNSValidationRecord `json:"dnsValidationRecords,omitempty"`
	NotBefore            string                `json:"notBefore,omitempty"`
	NotAfter             string                `json:"notAfter,omitempty"`
}

// ObservedState captures the live configuration of a AWS ACM Certificate
// as read from AWS Certificate Manager (ACM). It is compared against the spec
// during drift detection.
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

// ACMCertificateState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
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
