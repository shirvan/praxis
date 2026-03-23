# ACM Certificate Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages ACM Certificates, providing
> full lifecycle management including request, import, deletion, drift detection,
> and status polling for DNS and email validated public certificates and private
> CA-issued certificates.
>
> Key scope: `KeyScopeRegion` — key format is `region~name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned certificate
> ARN lives only in state/outputs.

---

## Table of Contents

1. [Overview & Scope](#1-overview--scope)
2. [Key Strategy](#2-key-strategy)
3. [File Inventory](#3-file-inventory)
4. [Step 1 — CUE Schema](#step-1--cue-schema)
5. [Step 2 — AWS Client Factory](#step-2--aws-client-factory)
6. [Step 3 — Driver Types](#step-3--driver-types)
7. [Step 4 — AWS API Abstraction Layer](#step-4--aws-api-abstraction-layer)
8. [Step 5 — Drift Detection](#step-5--drift-detection)
9. [Step 6 — Driver Implementation](#step-6--driver-implementation)
10. [Step 7 — Provider Adapter](#step-7--provider-adapter)
11. [Step 8 — Registry Integration](#step-8--registry-integration)
12. [Step 9 — Network Driver Pack Entry Point](#step-9--network-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [ACM-Certificate-Specific Design Decisions](#acm-certificate-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The ACM Certificate driver manages the lifecycle of AWS Certificate Manager
**certificates**. It requests, imports, updates metadata, and deletes certificates
along with their DNS validation records (as outputs), transparency logging options,
and tags.

ACM certificates are the foundational TLS/SSL primitives on AWS. A DNS-validated
certificate requires CNAME records in the domain's hosted zone — the driver outputs
these records for consumption by the Route 53 DNS Record driver, forming a DAG
dependency that Praxis resolves automatically. Once validation records propagate
and ACM validates them, the certificate transitions to `ISSUED` and can be attached
to ALB/NLB listeners.

**Out of scope**: Private CAs (ACM PCA), certificate associations on load balancers
(Listener driver responsibility), CloudFront certificate attachments, automatic
Route 53 CNAME creation. Each operates as a distinct resource or a concern of
another driver.

### Resource Scope for This Plan

| In Scope | Out of Scope (Other Drivers / AWS Managed) |
|---|---|
| Public certificate request | ACM Private CA |
| Private CA-issued certificate request | CloudFront certificate distribution |
| Certificate import (BYO cert) | Load balancer certificate association |
| DNS validation record outputs | Automatic Route 53 record creation |
| Email validation initiation | Email confirmation interaction |
| Certificate transparency logging options | Certificate renewal (AWS-managed) |
| Tags | ACM PCA configuration |
| Import and drift detection | |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Request or converge a certificate |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing certificate by ARN |
| `Delete` | `ObjectContext` (exclusive) | Delete a certificate |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (options, tags, status) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return certificate outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `domainName` | Immutable | Primary FQDN; changing requires a new certificate |
| `subjectAlternativeNames` | Immutable | SANs set at request time; cannot be updated in-place |
| `validationMethod` | Immutable | DNS or EMAIL; set at request time |
| `keyAlgorithm` | Immutable | RSA or ECDSA algorithm; set at request time |
| `certificateAuthorityArn` | Immutable | Determines public vs private CA cert |
| `options.certificateTransparencyLoggingPreference` | Mutable | Updated via `UpdateCertificateOptions` |
| `tags` | Mutable | Updated via `AddTagsToCertificate` / `RemoveTagsFromCertificate` |

### Downstream Consumers

```
${resources.my-cert.outputs.certificateArn}                               → Listener spec.certificateArn
${resources.my-cert.outputs.dnsValidationRecords[0].resourceRecordName}   → DNSRecord spec.name
${resources.my-cert.outputs.dnsValidationRecords[0].resourceRecordValue}  → DNSRecord spec.records[0]
${resources.my-cert.outputs.domainName}                                   → Cross-references / display
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

ACM certificates are regional resources. Certificate names are not an AWS concept
(certificates are identified by ARN), but Praxis uses `metadata.name` as the
logical identifier within a template. The key is `region~name`
(e.g., `us-east-1~api-cert`).

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `metadata.name`. Returns
  `region~name` (e.g., `us-east-1~api-cert`).

- **`BuildImportKey(region, resourceID)`**: `resourceID` is the certificate ARN or
  the UUID segment of the ARN. The adapter calls `DescribeCertificate` to retrieve
  the primary domain name, then returns `region~domainName` as the import key
  (using the domain as the Praxis name for imported certs, since certificates have
  no meaningful user-chosen name in AWS).

### Tag-Based Ownership

The driver tags each certificate with `praxis:managed-key=<region~name>` to:
1. Detect conflicts when provisioning a certificate with the same logical name
   across two Praxis installations targeting the same AWS account.
2. Enable `FindByManagedKey` lookups during import when only the logical key is
   known.

Because ACM `RequestCertificate` is **not idempotent** (repeated calls create
additional pending certificates rather than returning an existing one), the driver
must check for an existing managed certificate before requesting a new one:

1. Call `FindByManagedKey` to search for a certificate tagged with the managed key.
2. If found, use the existing ARN (already provisioned).
3. If not found, call `RequestCertificate` and tag immediately.

---

## 3. File Inventory

```text
✦ schemas/aws/acm/certificate.cue                           — CUE schema for ACMCertificate
✦ internal/drivers/acmcert/types.go                         — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/acmcert/aws.go                           — CertificateAPI interface + realCertificateAPI
✦ internal/drivers/acmcert/drift.go                         — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/acmcert/driver.go                        — ACMCertificateDriver Virtual Object
✦ internal/drivers/acmcert/driver_test.go                   — Unit tests for driver (mocked AWS)
✦ internal/drivers/acmcert/aws_test.go                      — Unit tests for error classification
✦ internal/drivers/acmcert/drift_test.go                    — Unit tests for drift detection
✦ internal/core/provider/acmcert_adapter.go                 — ACMCertificateAdapter implementing provider.Adapter
✦ internal/core/provider/acmcert_adapter_test.go            — Unit tests for adapter
✦ tests/integration/acm_certificate_driver_test.go          — Integration tests
✎ internal/infra/awsclient/client.go                        — Add NewACMClient factory
✎ cmd/praxis-network/main.go                                — Bind ACMCertificate driver
✎ internal/core/provider/registry.go                        — Add NewACMCertificateAdapter to NewRegistry()
✎ docker-compose.yaml                                       — Add acm to LocalStack SERVICES
✎ justfile                                                  — Add ACM build/test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/acm/certificate.cue`

```cue
package acm

import "strings"

#ACMCertificate: {
    apiVersion: "praxis.io/v1"
    kind:       "ACMCertificate"

    metadata: {
        // name is the logical name for this certificate within the Praxis template.
        // Used as the second segment of the Virtual Object key (region~name).
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the certificate is requested.
        region: string

        // domainName is the primary FQDN covered by the certificate.
        // Wildcard certificates use the "*.example.com" format.
        // Immutable after creation.
        domainName: string & =~"^(\*\.)?(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,})$"

        // subjectAlternativeNames lists additional FQDNs and wildcard domains
        // to include in the certificate alongside domainName.
        // Immutable after creation.
        subjectAlternativeNames?: [...string & =~"^(\*\.)?(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,})$"]

        // validationMethod controls how ACM validates domain ownership.
        // DNS is recommended; it integrates cleanly with the Route 53 DNS Record driver.
        // EMAIL validation requires manual action by domain registrant contacts.
        // Immutable after creation.
        validationMethod: "DNS" | "EMAIL" | *"DNS"

        // keyAlgorithm sets the public key algorithm and size for the certificate.
        // Immutable after creation.
        keyAlgorithm: "RSA_1024" | "RSA_2048" | "RSA_3072" | "RSA_4096" |
                      "EC_prime256v1" | "EC_secp384r1" | "EC_secp521r1" | *"RSA_2048"

        // certificateAuthorityArn is the ARN of an ACM Private CA.
        // When set, ACM issues a private certificate from the specified CA.
        // Mutually exclusive with public certificate issuance.
        // Immutable after creation.
        certificateAuthorityArn?: string & =~"^arn:aws(-cn|-us-gov)?:acm-pca:[a-z0-9-]+:[0-9]{12}:certificate-authority/.+$"

        // options controls certificate transparency logging.
        options?: {
            certificateTransparencyLoggingPreference?: "ENABLED" | "DISABLED" | *"ENABLED"
        }

        // tags applied to the certificate.
        tags: [string]: string
    }

    outputs?: {
        // certificateArn is the ARN of the ACM certificate.
        certificateArn: string

        // domainName is the primary domain name of the certificate.
        domainName: string

        // status is the current ACM lifecycle status of the certificate.
        // One of: PENDING_VALIDATION, ISSUED, INACTIVE, EXPIRED,
        //         VALIDATION_TIMED_OUT, REVOKED, FAILED.
        status: string

        // dnsValidationRecords contains the CNAME records ACM requires for
        // DNS validation. Each entry covers one or more SANs on the same
        // root domain. Populated after RequestCertificate; empty for EMAIL validation.
        dnsValidationRecords?: [...{
            domainName:          string
            resourceRecordName:  string
            resourceRecordType:  string
            resourceRecordValue: string
        }]

        // notBefore is the date from which the certificate is valid.
        notBefore?: string

        // notAfter is the certificate expiry date (ISO 8601).
        notAfter?: string
    }
}
```

### Key Design Decisions

- **`domainName` separate from `metadata.name`**: The domain is the AWS-level
  identifier for a certificate. `metadata.name` is the Praxis template resource
  name and forms the Virtual Object key. They may differ if the user wants a
  shorter template alias (e.g., `name: "api-tls"`, `domainName: "api.example.com"`).

- **`dnsValidationRecords` as a list of objects**: ACM returns one CNAME record per
  unique root domain. Wildcard and apex share a single CNAME. The structured output
  allows template expressions to reference specific record fields
  (`outputs.dnsValidationRecords[0].resourceRecordName`).

- **`options` as an optional block**: Certificate transparency logging is the only
  updatable option. Using an optional nested object avoids polluting the top-level
  spec while leaving room for future `options` fields.

- **`keyAlgorithm` defaulting to `RSA_2048`**: Maintains compatibility with the
  broadest set of clients. `EC_prime256v1` and `EC_secp384r1` are listed for users
  who need ECDSA for performance or compliance.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NEEDS NEW ACM CLIENT FACTORY**

```go
func NewACMClient(cfg aws.Config) *acm.Client {
    return acm.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/acm` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/acmcert/types.go`

```go
package acmcert

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "ACMCertificate"

// ACMCertificateSpec is the desired state for an ACM certificate.
type ACMCertificateSpec struct {
    Account                    string                     `json:"account,omitempty"`
    Region                     string                     `json:"region"`
    DomainName                 string                     `json:"domainName"`
    SubjectAlternativeNames    []string                   `json:"subjectAlternativeNames,omitempty"`
    ValidationMethod           string                     `json:"validationMethod"`
    KeyAlgorithm               string                     `json:"keyAlgorithm"`
    CertificateAuthorityArn    string                     `json:"certificateAuthorityArn,omitempty"`
    Options                    *CertificateOptions        `json:"options,omitempty"`
    Tags                       map[string]string          `json:"tags,omitempty"`
    ManagedKey                 string                     `json:"managedKey,omitempty"`
}

// CertificateOptions holds certificate transparency logging preferences.
type CertificateOptions struct {
    CertificateTransparencyLoggingPreference string `json:"certificateTransparencyLoggingPreference,omitempty"`
}

// DNSValidationRecord is a single CNAME record required by ACM for DNS validation.
type DNSValidationRecord struct {
    DomainName          string `json:"domainName"`
    ResourceRecordName  string `json:"resourceRecordName"`
    ResourceRecordType  string `json:"resourceRecordType"`
    ResourceRecordValue string `json:"resourceRecordValue"`
}

// ACMCertificateOutputs is produced after provisioning and stored in Restate K/V.
type ACMCertificateOutputs struct {
    CertificateArn      string                `json:"certificateArn"`
    DomainName          string                `json:"domainName"`
    Status              string                `json:"status"`
    DNSValidationRecords []DNSValidationRecord `json:"dnsValidationRecords,omitempty"`
    NotBefore           string                `json:"notBefore,omitempty"`
    NotAfter            string                `json:"notAfter,omitempty"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
    CertificateArn              string                `json:"certificateArn"`
    DomainName                  string                `json:"domainName"`
    SubjectAlternativeNames     []string              `json:"subjectAlternativeNames,omitempty"`
    ValidationMethod            string                `json:"validationMethod"`
    KeyAlgorithm                string                `json:"keyAlgorithm"`
    CertificateAuthorityArn     string                `json:"certificateAuthorityArn,omitempty"`
    Status                      string                `json:"status"`
    TransparencyLogging         string                `json:"transparencyLogging,omitempty"`
    DNSValidationRecords        []DNSValidationRecord `json:"dnsValidationRecords,omitempty"`
    NotBefore                   string                `json:"notBefore,omitempty"`
    NotAfter                    string                `json:"notAfter,omitempty"`
    Tags                        map[string]string     `json:"tags"`
}

// ACMCertificateState is the single atomic state object stored under drivers.StateKey.
type ACMCertificateState struct {
    Desired            ACMCertificateSpec      `json:"desired"`
    Observed           ObservedState           `json:"observed"`
    Outputs            ACMCertificateOutputs   `json:"outputs"`
    Status             types.ResourceStatus    `json:"status"`
    Mode               types.Mode              `json:"mode"`
    Error              string                  `json:"error,omitempty"`
    Generation         int64                   `json:"generation"`
    LastReconcile      string                  `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                    `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`DNSValidationRecord` as its own struct**: Surfaces the CNAME name, type, and
  value as structured fields rather than a JSON string, enabling direct template
  expression references like `outputs.dnsValidationRecords[0].resourceRecordName`.
- **`TransparencyLogging` in ObservedState**: `UpdateCertificateOptions` can change
  this value; tracking it in observed state enables drift detection.
- **`NotBefore`/`NotAfter` as strings**: ISO 8601 strings for simplified JSON
  serialization. These are informational outputs; the driver does not act on
  expiry (ACM handles renewal automatically for managed certs).
- **No `RenewalSummary` in ObservedState**: ACM managed renewal is fully automatic.
  The driver does not attempt to trigger or track renewals.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/acmcert/aws.go`

### CertificateAPI Interface

```go
type CertificateAPI interface {
    // RequestCertificate requests a new ACM certificate.
    // Returns the certificate ARN. NOT idempotent.
    RequestCertificate(ctx context.Context, spec ACMCertificateSpec) (string, error)

    // DescribeCertificate returns the full observed state of a certificate.
    DescribeCertificate(ctx context.Context, certArn string) (ObservedState, error)

    // DeleteCertificate deletes a certificate. Must not be in use by any AWS service.
    DeleteCertificate(ctx context.Context, certArn string) error

    // UpdateCertificateOptions updates mutable certificate options.
    // Currently only transparency logging preference can be changed.
    UpdateCertificateOptions(ctx context.Context, certArn string, opts CertificateOptions) error

    // UpdateTags replaces all user-managed tags on a certificate.
    UpdateTags(ctx context.Context, certArn string, tags map[string]string) error

    // FindByManagedKey searches for a certificate tagged with
    // praxis:managed-key=managedKey. Returns the certificate ARN or empty string.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realCertificateAPI Implementation

```go
type realCertificateAPI struct {
    client  *acm.Client
    limiter *ratelimit.Limiter
}

func NewCertificateAPI(client *acm.Client) CertificateAPI {
    return &realCertificateAPI{
        client:  client,
        limiter: ratelimit.New("acm-certificate", 10, 5),
    }
}
```

### Key Implementation Details

#### `RequestCertificate`

```go
func (r *realCertificateAPI) RequestCertificate(ctx context.Context, spec ACMCertificateSpec) (string, error) {
    r.limiter.Wait(ctx)
    input := &acm.RequestCertificateInput{
        DomainName:       aws.String(spec.DomainName),
        ValidationMethod: acmtypes.ValidationMethod(spec.ValidationMethod),
        KeyAlgorithm:     acmtypes.KeyAlgorithm(spec.KeyAlgorithm),
        Tags: []acmtypes.Tag{
            {Key: aws.String("praxis:managed-key"), Value: aws.String(spec.ManagedKey)},
        },
    }

    if len(spec.SubjectAlternativeNames) > 0 {
        input.SubjectAlternativeNames = spec.SubjectAlternativeNames
    }

    if spec.CertificateAuthorityArn != "" {
        input.CertificateAuthorityArn = aws.String(spec.CertificateAuthorityArn)
    }

    if spec.Options != nil && spec.Options.CertificateTransparencyLoggingPreference != "" {
        input.Options = &acmtypes.CertificateOptions{
            CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreference(
                spec.Options.CertificateTransparencyLoggingPreference,
            ),
        }
    }

    // Add user-supplied tags alongside the managed-key tag
    for k, v := range spec.Tags {
        input.Tags = append(input.Tags, acmtypes.Tag{
            Key:   aws.String(k),
            Value: aws.String(v),
        })
    }

    out, err := r.client.RequestCertificate(ctx, input)
    if err != nil {
        return "", err
    }

    return aws.ToString(out.CertificateArn), nil
}
```

> **Not idempotent**: Each `RequestCertificate` call creates a new certificate. The
> driver guards against duplicate requests by always calling `FindByManagedKey`
> inside `restate.Run` before invoking `RequestCertificate`. Because `restate.Run`
> journals the result, re-execution during replay returns the cached ARN without
> making another AWS call.

#### `DescribeCertificate`

```go
func (r *realCertificateAPI) DescribeCertificate(ctx context.Context, certArn string) (ObservedState, error) {
    r.limiter.Wait(ctx)
    out, err := r.client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
        CertificateArn: aws.String(certArn),
    })
    if err != nil {
        return ObservedState{}, err
    }

    cert := out.Certificate
    obs := ObservedState{
        CertificateArn:          aws.ToString(cert.CertificateArn),
        DomainName:              aws.ToString(cert.DomainName),
        SubjectAlternativeNames: cert.SubjectAlternativeNames,
        Status:                  string(cert.Status),
        ValidationMethod:        string(cert.DomainValidationOptions[0].ValidationMethod),
        KeyAlgorithm:            string(cert.KeyAlgorithm),
    }

    if cert.CertificateAuthorityArn != nil {
        obs.CertificateAuthorityArn = aws.ToString(cert.CertificateAuthorityArn)
    }

    if cert.Options != nil {
        obs.TransparencyLogging = string(cert.Options.CertificateTransparencyLoggingPreference)
    }

    if cert.NotBefore != nil {
        obs.NotBefore = cert.NotBefore.Format(time.RFC3339)
    }
    if cert.NotAfter != nil {
        obs.NotAfter = cert.NotAfter.Format(time.RFC3339)
    }

    // Extract DNS validation records — one per unique CNAME root
    seen := make(map[string]struct{})
    for _, dvo := range cert.DomainValidationOptions {
        if dvo.ResourceRecord == nil {
            continue
        }
        key := aws.ToString(dvo.ResourceRecord.Name)
        if _, ok := seen[key]; ok {
            continue // deduplicate — wildcard + apex share one CNAME
        }
        seen[key] = struct{}{}
        obs.DNSValidationRecords = append(obs.DNSValidationRecords, DNSValidationRecord{
            DomainName:          aws.ToString(dvo.DomainName),
            ResourceRecordName:  aws.ToString(dvo.ResourceRecord.Name),
            ResourceRecordType:  string(dvo.ResourceRecord.Type),
            ResourceRecordValue: aws.ToString(dvo.ResourceRecord.Value),
        })
    }

    // Fetch tags
    tagOut, err := r.client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
        CertificateArn: aws.String(certArn),
    })
    if err != nil {
        return ObservedState{}, fmt.Errorf("list tags for certificate %s: %w", certArn, err)
    }
    obs.Tags = make(map[string]string)
    for _, tag := range tagOut.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }

    return obs, nil
}
```

> **DNS validation records**: `DomainValidationOptions` is populated asynchronously
> after `RequestCertificate` — typically within a few seconds. The Provision handler
> polls `DescribeCertificate` with `restate.Sleep` until the `ResourceRecord` field
> is non-nil before writing outputs.

#### `DeleteCertificate`

```go
func (r *realCertificateAPI) DeleteCertificate(ctx context.Context, certArn string) error {
    r.limiter.Wait(ctx)

    _, err := r.client.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
        CertificateArn: aws.String(certArn),
    })
    return err
}
```

> **In-use guard**: `DeleteCertificate` returns `ResourceInUseException` if the
> certificate is currently associated with an AWS resource (load balancer, CloudFront
> distribution, etc.). The driver classifies this as a terminal error advising the
> user to remove the association before deleting the certificate.

#### `UpdateCertificateOptions`

```go
func (r *realCertificateAPI) UpdateCertificateOptions(ctx context.Context, certArn string, opts CertificateOptions) error {
    r.limiter.Wait(ctx)

    input := &acm.UpdateCertificateOptionsInput{
        CertificateArn: aws.String(certArn),
    }
    if opts.CertificateTransparencyLoggingPreference != "" {
        input.Options = &acmtypes.CertificateOptions{
            CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreference(
                opts.CertificateTransparencyLoggingPreference,
            ),
        }
    }
    _, err := r.client.UpdateCertificateOptions(ctx, input)
    return err
}
```

#### `UpdateTags`

```go
func (r *realCertificateAPI) UpdateTags(ctx context.Context, certArn string, tags map[string]string) error {
    r.limiter.Wait(ctx)
    // Get current tags to compute removals
    tagOut, err := r.client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
        CertificateArn: aws.String(certArn),
    })
    if err != nil {
        return fmt.Errorf("list tags: %w", err)
    }

    // Compute removals: keys in current but not in desired (preserve praxis: system tags)
    var removeTags []acmtypes.Tag
    for _, tag := range tagOut.Tags {
        key := aws.ToString(tag.Key)
        if strings.HasPrefix(key, "praxis:") {
            continue
        }
        if _, keep := tags[key]; !keep {
            removeTags = append(removeTags, tag)
        }
    }

    if len(removeTags) > 0 {
        if _, err := r.client.RemoveTagsFromCertificate(ctx, &acm.RemoveTagsFromCertificateInput{
            CertificateArn: aws.String(certArn),
            Tags:           removeTags,
        }); err != nil {
            return fmt.Errorf("remove tags: %w", err)
        }
    }

    if len(tags) > 0 {
        addTags := make([]acmtypes.Tag, 0, len(tags))
        for k, v := range tags {
            addTags = append(addTags, acmtypes.Tag{
                Key:   aws.String(k),
                Value: aws.String(v),
            })
        }
        if _, err := r.client.AddTagsToCertificate(ctx, &acm.AddTagsToCertificateInput{
            CertificateArn: aws.String(certArn),
            Tags:           addTags,
        }); err != nil {
            return fmt.Errorf("add tags: %w", err)
        }
    }

    return nil
}
```

#### `FindByManagedKey`

```go
func (r *realCertificateAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    var nextToken *string
    for {
        r.limiter.Wait(ctx)

        out, err := r.client.ListCertificates(ctx, &acm.ListCertificatesInput{
            NextToken: nextToken,
        })
        if err != nil {
            return "", err
        }

        for _, summary := range out.CertificateSummaryList {
            arn := aws.ToString(summary.CertificateArn)
            r.limiter.Wait(ctx)

            tagOut, err := r.client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
                CertificateArn: aws.String(arn),
            })
            if err != nil {
                continue
            }
            for _, tag := range tagOut.Tags {
                if aws.ToString(tag.Key) == "praxis:managed-key" &&
                    aws.ToString(tag.Value) == managedKey {
                    return arn, nil
                }
            }
        }

        if out.NextToken == nil {
            break
        }
        nextToken = out.NextToken
    }
    return "", nil
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var rnfe *acmtypes.ResourceNotFoundException
    if errors.As(err, &rnfe) {
        return true
    }
    return strings.Contains(err.Error(), "ResourceNotFoundException") ||
        strings.Contains(err.Error(), "NotFound")
}

func isLimitExceeded(err error) bool {
    var lee *acmtypes.LimitExceededException
    if errors.As(err, &lee) {
        return true
    }
    return strings.Contains(err.Error(), "LimitExceeded")
}

func isInvalidArn(err error) bool {
    var iae *acmtypes.InvalidArnException
    if errors.As(err, &iae) {
        return true
    }
    return strings.Contains(err.Error(), "InvalidArn")
}

func isInvalidDomain(err error) bool {
    var idvoe *acmtypes.InvalidDomainValidationOptionsException
    if errors.As(err, &idvoe) {
        return true
    }
    return strings.Contains(err.Error(), "InvalidDomainValidationOptions")
}

func isInvalidState(err error) bool {
    var ise *acmtypes.InvalidStateException
    if errors.As(err, &ise) {
        return true
    }
    return strings.Contains(err.Error(), "InvalidState")
}

func isResourceInUse(err error) bool {
    var riue *acmtypes.ResourceInUseException
    if errors.As(err, &riue) {
        return true
    }
    return strings.Contains(err.Error(), "ResourceInUse")
}

func isRequestInProgress(err error) bool {
    return strings.Contains(err.Error(), "RequestInProgress")
}

func isThrottled(err error) bool {
    return strings.Contains(err.Error(), "Throttl") ||
        strings.Contains(err.Error(), "TooManyRequests") ||
        strings.Contains(err.Error(), "RateExceeded")
}

// classifyError maps ACM API errors to terminal or retryable categories.
func classifyError(err error) error {
    if isNotFound(err) {
        return restate.TerminalError(err, 404)
    }
    if isLimitExceeded(err) {
        return restate.TerminalError(
            fmt.Errorf("ACM certificate limit exceeded; request a quota increase: %w", err), 429)
    }
    if isInvalidArn(err) || isInvalidDomain(err) {
        return restate.TerminalError(err, 400)
    }
    if isInvalidState(err) {
        return restate.TerminalError(err, 409)
    }
    if isResourceInUse(err) {
        return restate.TerminalError(
            fmt.Errorf("certificate is still associated with an AWS resource; remove associations before deleting: %w", err), 409)
    }
    // Retryable: RequestInProgress, Throttled, transient AWS errors
    return err
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/acmcert/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| `options.certificateTransparencyLoggingPreference` | External change via console/CLI | Updated via `UpdateCertificateOptions` |
| `tags` | External change via console/CLI | Key-value pairs via `AddTagsToCertificate` / `RemoveTagsFromCertificate` |

> **Not drift-detected**: `domainName`, `subjectAlternativeNames`, `validationMethod`,
> `keyAlgorithm`, `certificateAuthorityArn` — all immutable after creation. If these
> differ between desired and observed, the driver logs a warning but does not attempt
> to correct them (no AWS API exists to do so).

> **Certificate status**: `EXPIRED`, `REVOKED`, or `FAILED` status observed during
> `Reconcile` is reported as an error in `GetStatus` but is not treated as a
> correctable drift condition (renewal is AWS-managed; revocation is irreversible).

### HasDrift

```go
func HasDrift(desired ACMCertificateSpec, observed ObservedState) bool {
    if desired.Options != nil {
        if desired.Options.CertificateTransparencyLoggingPreference != "" &&
            desired.Options.CertificateTransparencyLoggingPreference != observed.TransparencyLogging {
            return true
        }
    }
    return !tagsEqual(desired.Tags, observed.Tags)
}

func tagsEqual(desired, observed map[string]string) bool {
    // Strip praxis: system tags from observed before comparing
    filtered := make(map[string]string)
    for k, v := range observed {
        if !strings.HasPrefix(k, "praxis:") {
            filtered[k] = v
        }
    }
    if len(desired) != len(filtered) {
        return false
    }
    for k, v := range desired {
        if filtered[k] != v {
            return false
        }
    }
    return true
}
```

### ComputeFieldDiffs

```go
type FieldDiffEntry struct {
    Field   string
    Desired string
    Actual  string
}

func ComputeFieldDiffs(desired ACMCertificateSpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry

    if desired.Options != nil &&
        desired.Options.CertificateTransparencyLoggingPreference != "" &&
        desired.Options.CertificateTransparencyLoggingPreference != observed.TransparencyLogging {
        diffs = append(diffs, FieldDiffEntry{
            Field:   "options.certificateTransparencyLoggingPreference",
            Desired: desired.Options.CertificateTransparencyLoggingPreference,
            Actual:  observed.TransparencyLogging,
        })
    }

    if !tagsEqual(desired.Tags, observed.Tags) {
        diffs = append(diffs, FieldDiffEntry{
            Field:   "tags",
            Desired: fmt.Sprintf("%v", desired.Tags),
            Actual:  fmt.Sprintf("%v", filteredTags(observed.Tags)),
        })
    }

    return diffs
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/acmcert/driver.go`

### ACMCertificateDriver Struct

```go
type ACMCertificateDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) CertificateAPI
}

func NewACMCertificateDriver(accounts *auth.Registry) *ACMCertificateDriver {
    return NewACMCertificateDriverWithFactory(accounts, func(cfg aws.Config) CertificateAPI {
        return NewCertificateAPI(awsclient.NewACMClient(cfg))
    })
}

func NewACMCertificateDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) CertificateAPI) *ACMCertificateDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) CertificateAPI {
            return NewCertificateAPI(awsclient.NewACMClient(cfg))
        }
    }
    return &ACMCertificateDriver{auth: accounts, apiFactory: factory}
}

func (d *ACMCertificateDriver) apiForAccount(account string) (CertificateAPI, error) {
    if d == nil || d.auth == nil || d.apiFactory == nil {
        return nil, fmt.Errorf("ACMCertificateDriver is not configured with an auth registry")
    }
    awsCfg, err := d.auth.Resolve(account)
    if err != nil {
        return nil, fmt.Errorf("resolve ACM account %q: %w", account, err)
    }
    return d.apiFactory(awsCfg), nil
}
```

### Provision Handler

```go
func (d *ACMCertificateDriver) Provision(ctx restate.ObjectContext, spec ACMCertificateSpec) (ACMCertificateOutputs, error) {
    api := d.api(ctx)
    spec.ManagedKey = restate.Key(ctx)

    // 1. Load existing state (idempotency guard)
    state, err := restate.Get[*ACMCertificateState](ctx, drivers.StateKey)
    if err != nil {
        return ACMCertificateOutputs{}, err
    }

    var certArn string

    if state != nil && state.Outputs.CertificateArn != "" {
        // Already provisioned; update if drift exists
        certArn = state.Outputs.CertificateArn
    } else {
        // Check for existing managed certificate to prevent duplicates
        existing, err := restate.Run(ctx, func(ctx restate.RunContext) (string, error) {
            arn, err := api.FindByManagedKey(ctx, spec.ManagedKey)
            if err != nil {
                return "", classifyError(err)
            }
            return arn, nil
        })
        if err != nil {
            return ACMCertificateOutputs{}, err
        }

        if existing != "" {
            certArn = existing
        } else {
            // Request new certificate
            arn, err := restate.Run(ctx, func(ctx restate.RunContext) (string, error) {
                a, err := api.RequestCertificate(ctx, spec)
                if err != nil {
                    return "", classifyError(err)
                }
                return a, nil
            })
            if err != nil {
                return ACMCertificateOutputs{}, err
            }
            certArn = arn
        }
    }

    // 2. Poll until DNS validation records are available (for DNS validation method)
    var obs ObservedState
    if spec.ValidationMethod == "DNS" {
        for {
            o, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
                o, err := api.DescribeCertificate(ctx, certArn)
                if err != nil {
                    return ObservedState{}, classifyError(err)
                }
                return o, nil
            })
            if err != nil {
                return ACMCertificateOutputs{}, err
            }
            obs = o
            if len(obs.DNSValidationRecords) > 0 {
                break
            }
            // DNS validation options not yet populated; wait before retrying
            if err := restate.Sleep(ctx, 5*time.Second); err != nil {
                return ACMCertificateOutputs{}, err
            }
        }
    } else {
        o, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
            o, err := api.DescribeCertificate(ctx, certArn)
            if err != nil {
                return ObservedState{}, classifyError(err)
            }
            return o, nil
        })
        if err != nil {
            return ACMCertificateOutputs{}, err
        }
        obs = o
    }

    // 3. Apply drift corrections if re-provisioning
    if HasDrift(spec, obs) {
        diffs := ComputeFieldDiffs(spec, obs)
        for _, diff := range diffs {
            d := diff // capture
            switch d.Field {
            case "options.certificateTransparencyLoggingPreference":
                if _, err := restate.Run(ctx, func(ctx restate.RunContext) (restate.Void, error) {
                    if err := api.UpdateCertificateOptions(ctx, certArn, CertificateOptions{
                        CertificateTransparencyLoggingPreference: spec.Options.CertificateTransparencyLoggingPreference,
                    }); err != nil {
                        return restate.Void{}, classifyError(err)
                    }
                    return restate.Void{}, nil
                }); err != nil {
                    return ACMCertificateOutputs{}, err
                }
            case "tags":
                if _, err := restate.Run(ctx, func(ctx restate.RunContext) (restate.Void, error) {
                    if err := api.UpdateTags(ctx, certArn, spec.Tags); err != nil {
                        return restate.Void{}, classifyError(err)
                    }
                    return restate.Void{}, nil
                }); err != nil {
                    return ACMCertificateOutputs{}, err
                }
            }
        }
    }

    // 4. Build and persist outputs
    outputs := ACMCertificateOutputs{
        CertificateArn:       certArn,
        DomainName:           obs.DomainName,
        Status:               obs.Status,
        DNSValidationRecords: obs.DNSValidationRecords,
        NotBefore:            obs.NotBefore,
        NotAfter:             obs.NotAfter,
    }

    newState := &ACMCertificateState{
        Desired:    spec,
        Observed:   obs,
        Outputs:    outputs,
        Status:     types.StatusActive,
        Mode:       types.ModeManaged,
        Generation: func() int64 {
            if state != nil {
                return state.Generation + 1
            }
            return 1
        }(),
    }
    restate.Set(ctx, drivers.StateKey, newState)

    return outputs, nil
}
```

### Delete Handler

```go
func (d *ACMCertificateDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*ACMCertificateState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state == nil || state.Outputs.CertificateArn == "" {
        return nil // nothing to delete
    }

    api := d.api(ctx)
    certArn := state.Outputs.CertificateArn

    if _, err := restate.Run(ctx, func(ctx restate.RunContext) (restate.Void, error) {
        if err := api.DeleteCertificate(ctx, certArn); err != nil {
            if isNotFound(err) {
                return restate.Void{}, nil
            }
            return restate.Void{}, classifyError(err)
        }
        return restate.Void{}, nil
    }); err != nil {
        return err
    }

    restate.ClearAll(ctx)
    return nil
}
```

### Import Handler

```go
func (d *ACMCertificateDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ACMCertificateOutputs, error) {
    // ref.ID is the certificate ARN
    certArn := ref.ID
    api := d.api(ctx)

    obs, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
        o, err := api.DescribeCertificate(ctx, certArn)
        if err != nil {
            return ObservedState{}, classifyError(err)
        }
        return o, nil
    })
    if err != nil {
        return ACMCertificateOutputs{}, err
    }

    outputs := ACMCertificateOutputs{
        CertificateArn:       certArn,
        DomainName:           obs.DomainName,
        Status:               obs.Status,
        DNSValidationRecords: obs.DNSValidationRecords,
        NotBefore:            obs.NotBefore,
        NotAfter:             obs.NotAfter,
    }

    specFromObs := ACMCertificateSpec{
        Region:                  strings.SplitN(restate.Key(ctx), "~", 2)[0],
        DomainName:              obs.DomainName,
        SubjectAlternativeNames: obs.SubjectAlternativeNames,
        ValidationMethod:        obs.ValidationMethod,
        KeyAlgorithm:            obs.KeyAlgorithm,
        CertificateAuthorityArn: obs.CertificateAuthorityArn,
        Tags:                    filteredTags(obs.Tags),
        ManagedKey:              restate.Key(ctx),
    }
    if obs.TransparencyLogging != "" {
        specFromObs.Options = &CertificateOptions{
            CertificateTransparencyLoggingPreference: obs.TransparencyLogging,
        }
    }

    restate.Set(ctx, drivers.StateKey, &ACMCertificateState{
        Desired:  specFromObs,
        Observed: obs,
        Outputs:  outputs,
        Status:   types.StatusActive,
        Mode:     types.ModeObserved,
    })

    return outputs, nil
}
```

### Reconcile Handler

```go
func (d *ACMCertificateDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*ACMCertificateState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Changed: false}, nil
    }

    api := d.api(ctx)
    certArn := state.Outputs.CertificateArn

    obs, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
        o, err := api.DescribeCertificate(ctx, certArn)
        if err != nil {
            return ObservedState{}, classifyError(err)
        }
        return o, nil
    })
    if err != nil {
        return types.ReconcileResult{}, err
    }

    // Report terminal certificate states
    switch obs.Status {
    case "EXPIRED", "REVOKED", "FAILED", "VALIDATION_TIMED_OUT":
        state.Observed = obs
        state.Status = types.StatusError
        state.Error = fmt.Sprintf("certificate status: %s", obs.Status)
        state.Outputs.Status = obs.Status
        restate.Set(ctx, drivers.StateKey, state)
        return types.ReconcileResult{Changed: true}, nil
    case "ISSUED":
        if state.Status != types.StatusActive {
            state.Status = types.StatusActive
            state.Error = ""
            state.Outputs.Status = obs.Status
            state.Outputs.NotBefore = obs.NotBefore
            state.Outputs.NotAfter = obs.NotAfter
        }
    }

    if state.Mode == types.ModeObserved {
        // Observed mode: detect drift but do not correct
        diffs := ComputeFieldDiffs(state.Desired, obs)
        state.Observed = obs
        state.Outputs.Status = obs.Status
        restate.Set(ctx, drivers.StateKey, state)
        if len(diffs) > 0 {
            return types.ReconcileResult{Changed: true, Diffs: formatDiffs(diffs)}, nil
        }
        return types.ReconcileResult{Changed: false}, nil
    }

    // Managed mode: correct drift
    if !HasDrift(state.Desired, obs) {
        state.Observed = obs
        state.Outputs.Status = obs.Status
        restate.Set(ctx, drivers.StateKey, state)
        return types.ReconcileResult{Changed: false}, nil
    }

    // Re-provision to converge
    outputs, err := d.Provision(ctx, state.Desired)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    return types.ReconcileResult{Changed: true, Outputs: outputs}, nil
}
```

### GetStatus Handler

```go
func (d *ACMCertificateDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
    state, err := restate.Get[*ACMCertificateState](ctx, drivers.StateKey)
    if err != nil {
        return types.StatusResponse{}, err
    }
    if state == nil {
        return types.StatusResponse{Status: types.StatusNotFound}, nil
    }
    return types.StatusResponse{
        Status:     state.Status,
        Error:      state.Error,
        Generation: state.Generation,
        Outputs:    state.Outputs,
    }, nil
}
```

### GetOutputs Handler

```go
func (d *ACMCertificateDriver) GetOutputs(ctx restate.ObjectSharedContext) (ACMCertificateOutputs, error) {
    state, err := restate.Get[*ACMCertificateState](ctx, drivers.StateKey)
    if err != nil {
        return ACMCertificateOutputs{}, err
    }
    if state == nil {
        return ACMCertificateOutputs{}, restate.TerminalError(
            fmt.Errorf("certificate not found"), 404)
    }
    return state.Outputs, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/acmcert_adapter.go`

```go
package provider

import (
    "encoding/json"
    "fmt"

    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"

    "github.com/praxiscloud/praxis/internal/core/auth"
    "github.com/praxiscloud/praxis/internal/drivers/acmcert"
    "github.com/praxiscloud/praxis/internal/infra/awsclient"
    "github.com/praxiscloud/praxis/pkg/types"
)

type ACMCertificateAdapter struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) acmcert.CertificateAPI
}

func NewACMCertificateAdapterWithRegistry(accounts *auth.Registry) *ACMCertificateAdapter {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    return &ACMCertificateAdapter{
        auth: accounts,
        apiFactory: func(cfg aws.Config) acmcert.CertificateAPI {
            return acmcert.NewCertificateAPI(awsclient.NewACMClient(cfg))
        },
    }
}

func (a *ACMCertificateAdapter) Kind() string {
    return acmcert.ServiceName
}

func (a *ACMCertificateAdapter) ServiceName() string {
    return acmcert.ServiceName
}

func (a *ACMCertificateAdapter) Scope() KeyScope {
    return KeyScopeRegion
}

func (a *ACMCertificateAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    var doc struct {
        Metadata struct{ Name string `json:"name"` } `json:"metadata"`
        Spec     struct{ Region string `json:"region"` } `json:"spec"`
    }
    if err := json.Unmarshal(resourceDoc, &doc); err != nil {
        return "", fmt.Errorf("ACMCertificateAdapter.BuildKey: %w", err)
    }
    return JoinKey(doc.Spec.Region, doc.Metadata.Name), nil
}

func (a *ACMCertificateAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // resourceID is a certificate ARN or the UUID segment of the ARN.
    // Use region~resourceID as the import key (domain name unknown at import time).
    return JoinKey(region, resourceID), nil
}

func (a *ACMCertificateAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    return decodeSpec[acmcert.ACMCertificateSpec](resourceDoc)
}

func (a *ACMCertificateAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
    // Typed call to ACMCertificate.Provision via Restate service-to-service
    // (implementation follows standard adapter pattern)
    ...
}

func (a *ACMCertificateAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
    ...
}

func (a *ACMCertificateAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
    ...
}

func (a *ACMCertificateAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    ...
}

func (a *ACMCertificateAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
    ...
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

```go
func NewRegistry() *Registry {
    accounts := auth.LoadFromEnv()
    return NewRegistryWithAdapters(
        // ... existing adapters ...
        NewACMCertificateAdapterWithRegistry(accounts),
    )
}
```

---

## Step 9 — Network Driver Pack Entry Point

**File**: `cmd/praxis-network/main.go`

```go
// Bind ACMCertificate driver alongside existing network drivers
srv := server.NewRestate().
    // ... existing bindings ...
    Bind(restate.Reflect(acmcert.NewACMCertificateDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### docker-compose.yaml

```yaml
localstack:
  environment:
    # Add acm to existing SERVICES list
    - SERVICES=s3,ssm,sts,ec2,iam,route53,acm,...
```

### justfile

```just
# Unit tests
test-acmcert:
    go test ./internal/drivers/acmcert/... -v -count=1 -race

# Integration tests
test-acmcert-integration:
    go test ./tests/integration/ -run TestACMCertificate \
            -v -count=1 -tags=integration -timeout=5m

# Build (included in existing build-network)
build-network:
    go build -o bin/praxis-network ./cmd/praxis-network
```

---

## Step 11 — Unit Tests

**Files**: `internal/drivers/acmcert/driver_test.go`, `aws_test.go`, `drift_test.go`

### driver_test.go — Core Scenarios

```go
// TestProvision_NewCertificate — first-time provision with DNS validation
// TestProvision_Idempotent — re-provision returns existing ARN (FindByManagedKey returns existing)
// TestProvision_DriftCorrection — re-provision applies transparency logging change
// TestProvision_PollingForDNSRecords — restate.Sleep loop until ResourceRecord populated
// TestDelete_Success — DeleteCertificate called with correct ARN; state cleared
// TestDelete_NotFound — certificate already absent; no error
// TestDelete_InUse — ResourceInUseException → terminal error
// TestImport_Success — DescribeCertificate adopted; mode = ModeObserved
// TestReconcile_IssuedStatus — PENDING_VALIDATION → ISSUED updates status + notBefore/notAfter
// TestReconcile_ExpiredStatus — EXPIRED → StatusError
// TestReconcile_TagDrift_Managed — tags reconciled in managed mode
// TestReconcile_TagDrift_Observed — drift reported but not corrected in observed mode
// TestGetStatus_NotProvisioned — returns StatusNotFound
// TestGetOutputs_Success — returns stored outputs
```

### aws_test.go — Error Classification

```go
// TestClassifyError_NotFound
// TestClassifyError_LimitExceeded
// TestClassifyError_InvalidArn
// TestClassifyError_InvalidDomain
// TestClassifyError_InvalidState
// TestClassifyError_ResourceInUse
// TestClassifyError_Throttled_Retryable
```

### drift_test.go — Drift Detection

```go
// TestHasDrift_NoDrift
// TestHasDrift_TransparencyLoggingChanged
// TestHasDrift_TagAdded
// TestHasDrift_TagRemoved
// TestHasDrift_TagValueChanged
// TestHasDrift_SystemTagsIgnored
// TestComputeFieldDiffs_TransparencyLogging
// TestComputeFieldDiffs_Tags
// TestTagsEqual_DeduplicatesSystemTags
```

---

## Step 12 — Integration Tests

**File**: `tests/integration/acm_certificate_driver_test.go`

```go
// TestACMCertificate_RequestAndDescribe
//   1. Provision ACMCertificate with DNS validation
//   2. Check outputs.certificateArn is non-empty
//   3. Check outputs.dnsValidationRecords has at least one entry
//   4. Verify status == PENDING_VALIDATION (LocalStack does not auto-validate)

// TestACMCertificate_IdempotentProvision
//   1. Provision twice with the same managed key
//   2. Verify same ARN returned on second provision (FindByManagedKey short-circuits)

// TestACMCertificate_UpdateTransparencyLogging
//   1. Provision with ENABLED
//   2. Re-provision with DISABLED
//   3. Verify DescribeCertificate reflects DISABLED

// TestACMCertificate_UpdateTags
//   1. Provision with tags {env: prod}
//   2. Re-provision with tags {env: staging, team: platform}
//   3. Verify ListTagsForCertificate returns new tag set; old tag removed

// TestACMCertificate_Delete
//   1. Provision a certificate
//   2. Call Delete
//   3. Verify DescribeCertificate returns ResourceNotFoundException

// TestACMCertificate_Import
//   1. Create certificate via raw AWS SDK (simulate externally created cert)
//   2. Call Import handler with the certificate ARN
//   3. Verify state.Mode == ModeObserved, outputs populated

// TestACMCertificate_GetStatus_NotFound
//   1. Call GetStatus on an unprovisioned key
//   2. Expect StatusNotFound

// TestACMCertificate_Reconcile_Drift
//   1. Provision certificate
//   2. Directly apply tag change via AWS SDK (simulate manual change)
//   3. Call Reconcile
//   4. Verify tag drift is corrected (managed mode)
```

---

## ACM-Certificate-Specific Design Decisions

### Non-Idempotent RequestCertificate

Unlike SNS `CreateTopic` or EC2 `RunInstances` with idempotency tokens, ACM
`RequestCertificate` creates a new certificate each time it is called. To prevent
duplicate certificates during retry or replay:

1. The driver calls `FindByManagedKey` inside `restate.Run` before calling
   `RequestCertificate`.
2. Restate journals the result of `FindByManagedKey`; during replay the journaled
   ARN is returned immediately without re-executing `FindByManagedKey`.
3. If `FindByManagedKey` returns empty, `RequestCertificate` is called in a
   separate `restate.Run`; the resulting ARN is journaled.

This two-step approach ensures exactly-once semantics despite the non-idempotent
AWS API.

### Polling for DNS Validation Records

After `RequestCertificate` returns, ACM populates `DomainValidationOptions`
asynchronously (typically within seconds). The Provision handler polls with
`restate.Sleep(5 * time.Second)` until `ResourceRecord` is non-nil. This loop
is bounded by Restate's durable execution — the driver will resume from the last
sleep if interrupted without re-requesting the certificate.

The outputs include the DNS validation records in `PENDING_VALIDATION` status,
allowing the user to create Route 53 DNS records and trigger validation without
waiting for the driver to reach `ISSUED`.

### Status-Based Output Updates

ACM certificates are provisioned immediately (`PENDING_VALIDATION`) but only
become fully usable after validation (`ISSUED`). The driver design:

- `Provision` returns outputs in `PENDING_VALIDATION` state — DNS validation records
  are available so the user can immediately create the required Route 53 records.
- `Reconcile` polls `DescribeCertificate` and updates the stored status and validity
  dates when the certificate transitions to `ISSUED`.
- Downstream resources (Listener with `certificateArn`) should have a DAG edge on
  the validation DNS record, not directly on the certificate, to avoid attaching an
  unvalidated certificate to a load balancer.

### Immutable Fields Warning vs Terminal Error

When `Reconcile` detects a drift on an immutable field (e.g., the domain name was
changed in the spec after the certificate was issued), the driver logs a warning
and reports the drift in `ReconcileResult.Diffs` but does not treat it as a
terminal error. The user is responsible for replacing the resource (delete + create
with a new Praxis name) when immutable field changes are needed.

### Certificate Lifecycle States Mapping

| ACM Status | Praxis Status | Action |
|---|---|---|
| `PENDING_VALIDATION` | `types.StatusPending` | DNS records in outputs; user creates CNAME |
| `ISSUED` | `types.StatusActive` | Ready for use; notBefore/notAfter populated |
| `INACTIVE` | `types.StatusInactive` | Certificate exists but not in use |
| `EXPIRED` | `types.StatusError` | AWS does not auto-renew non-FQDN or imported certs |
| `VALIDATION_TIMED_OUT` | `types.StatusError` | Validation not completed within 72 hours |
| `FAILED` | `types.StatusError` | RequestCertificate failed; failureReason in error |
| `REVOKED` | `types.StatusError` | Permanently revoked; must delete and re-create |

---

## Design Decisions (Resolved)

| Decision | Resolution |
|---|---|
| Key scope | `KeyScopeRegion` — certificates are regional; `region~name` |
| Second resource type? | No separate ACM Validation resource. DNS validation records are outputs of the certificate driver, consumed by Route 53 DNS Record driver |
| Wait for ISSUED in Provision? | No — Provision completes at `PENDING_VALIDATION` and outputs DNS records immediately. `Reconcile` tracks the transition to `ISSUED` |
| Private CA certificates | Same driver; `certificateAuthorityArn` field triggers private issuance. Same lifecycle except no public CNAME validation |
| Certificate import | `Import` handler uses `ImportCertificate` pattern with `DescribeCertificate`; sets `ModeObserved` |
| SANs mutability | Immutable. AWS does not support in-place SAN update. User must delete and recreate |
| Rate limiter | `acm-certificate`, 10 sustained / 5 burst |
| Pack assignment | `praxis-network` — TLS/SSL certificates are network-layer primitives |

---

## Checklist

### Schema
- [ ] `schemas/aws/acm/certificate.cue` — `#ACMCertificate` definition

### Driver Files
- [ ] `internal/drivers/acmcert/types.go` — Spec, Outputs, ObservedState, State
- [ ] `internal/drivers/acmcert/aws.go` — `CertificateAPI` interface + `realCertificateAPI`
- [ ] `internal/drivers/acmcert/drift.go` — `HasDrift`, `ComputeFieldDiffs`, `FieldDiffEntry`
- [ ] `internal/drivers/acmcert/driver.go` — `ACMCertificateDriver` Virtual Object
- [ ] `internal/drivers/acmcert/driver_test.go` — Unit tests
- [ ] `internal/drivers/acmcert/aws_test.go` — Error classification tests
- [ ] `internal/drivers/acmcert/drift_test.go` — Drift detection tests

### Provider Adapter
- [ ] `internal/core/provider/acmcert_adapter.go` — `ACMCertificateAdapter`
- [ ] `internal/core/provider/acmcert_adapter_test.go` — Adapter unit tests

### Registry
- [ ] `NewACMCertificateAdapterWithRegistry` registered in `NewRegistry()`

### Infrastructure
- [ ] `internal/infra/awsclient/client.go` — `NewACMClient()` factory added
- [ ] `cmd/praxis-network/main.go` — `ACMCertificateDriver` bound
- [ ] `docker-compose.yaml` — `acm` added to LocalStack `SERVICES`
- [ ] `justfile` — `test-acmcert` and `test-acmcert-integration` targets added

### Tests
- [ ] All unit test scenarios passing
- [ ] All integration test scenarios passing against LocalStack

### Documentation
- [x] This implementation plan
- [x] `ACM_DRIVER_PACK_OVERVIEW.md`
