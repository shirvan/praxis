package acmcert

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ACMCertificateDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) CertificateAPI
}

func NewACMCertificateDriver(auth authservice.AuthClient) *ACMCertificateDriver {
	return NewACMCertificateDriverWithFactory(auth, func(cfg aws.Config) CertificateAPI {
		return NewCertificateAPI(awsclient.NewACMClient(cfg))
	})
}

func NewACMCertificateDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) CertificateAPI) *ACMCertificateDriver {
	if factory == nil {
		factory = func(cfg aws.Config) CertificateAPI { return NewCertificateAPI(awsclient.NewACMClient(cfg)) }
	}
	return &ACMCertificateDriver{auth: auth, apiFactory: factory}
}

func (d *ACMCertificateDriver) ServiceName() string {
	return ServiceName
}

func (d *ACMCertificateDriver) Provision(ctx restate.ObjectContext, spec ACMCertificateSpec) (ACMCertificateOutputs, error) {
	ctx.Log().Info("provisioning ACM certificate", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return ACMCertificateOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if strings.TrimSpace(spec.Region) == "" {
		spec.Region = region
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ACMCertificateOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	state, err := restate.Get[ACMCertificateState](ctx, drivers.StateKey)
	if err != nil {
		return ACMCertificateOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	certificateArn := state.Outputs.CertificateArn
	observed := state.Observed
	if certificateArn != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeCertificate(rc, certificateArn)
		})
		if descErr == nil {
			observed = described
		} else {
			certificateArn = ""
		}
	}
	if certificateArn == "" && spec.ManagedKey != "" {
		conflictArn, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if findErr != nil {
			state.Status = types.StatusError
			state.Error = findErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ACMCertificateOutputs{}, findErr
		}
		if conflictArn != "" {
			err := formatManagedKeyConflict(spec.ManagedKey, conflictArn)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ACMCertificateOutputs{}, restate.TerminalError(err, 409)
		}
	}
	if certificateArn == "" {
		createdArn, runErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			arn, reqErr := api.RequestCertificate(rc, spec)
			if reqErr != nil {
				if IsInvalidDomain(reqErr) || IsInvalidArn(reqErr) {
					return "", restate.TerminalError(reqErr, 400)
				}
				if IsQuotaExceeded(reqErr) {
					return "", restate.TerminalError(reqErr, 409)
				}
				if IsConflict(reqErr) || IsInvalidState(reqErr) {
					return "", restate.TerminalError(reqErr, 409)
				}
				return "", reqErr
			}
			return arn, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ACMCertificateOutputs{}, runErr
		}
		certificateArn = createdArn
	}
	if observed.CertificateArn != "" {
		if err := validateImmutableFields(spec, observed); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ACMCertificateOutputs{}, restate.TerminalError(err, 409)
		}
		if correctionErr := d.correctDrift(ctx, api, certificateArn, spec, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			state.Outputs = ACMCertificateOutputs{CertificateArn: certificateArn}
			restate.Set(ctx, drivers.StateKey, state)
			return ACMCertificateOutputs{}, correctionErr
		}
	}
	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeCertificate(rc, certificateArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = ACMCertificateOutputs{CertificateArn: certificateArn}
		restate.Set(ctx, drivers.StateKey, state)
		return ACMCertificateOutputs{}, err
	}
	outputs := outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *ACMCertificateDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ACMCertificateOutputs, error) {
	ctx.Log().Info("importing ACM certificate", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return ACMCertificateOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultACMCertificateImportMode(ref.Mode)
	state, err := restate.Get[ACMCertificateState](ctx, drivers.StateKey)
	if err != nil {
		return ACMCertificateOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeCertificate(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: certificate %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return ACMCertificateOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *ACMCertificateDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting ACM certificate", "key", restate.Key(ctx))
	state, err := restate.Get[ACMCertificateState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete ACM certificate %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.CertificateArn), 409)
	}
	certificateArn := state.Outputs.CertificateArn
	if certificateArn == "" {
		restate.Set(ctx, drivers.StateKey, ACMCertificateState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteCertificate(rc, certificateArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsConflict(runErr) || IsInvalidState(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
			if IsInvalidArn(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, ACMCertificateState{Status: types.StatusDeleted})
	return nil
}

func (d *ACMCertificateDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ACMCertificateState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	certificateArn := state.Outputs.CertificateArn
	if certificateArn == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	describe, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeCertificate(rc, certificateArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("ACM certificate %s was deleted externally", certificateArn)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	state.Observed = describe
	state.Outputs = outputsFromObserved(describe)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, describe)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.correctDrift(ctx, api, certificateArn, state.Desired, describe); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

func (d *ACMCertificateDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ACMCertificateState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *ACMCertificateDriver) GetOutputs(ctx restate.ObjectSharedContext) (ACMCertificateOutputs, error) {
	state, err := restate.Get[ACMCertificateState](ctx, drivers.StateKey)
	if err != nil {
		return ACMCertificateOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *ACMCertificateDriver) correctDrift(ctx restate.ObjectContext, api CertificateAPI, certificateArn string, desired ACMCertificateSpec, observed ObservedState) error {
	if normalizeTransparencyPreference(desired.Options) != normalizeTransparencyPreference(&observed.Options) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateCertificateOptions(rc, certificateArn, desired.Options)
		})
		if err != nil {
			return fmt.Errorf("update certificate options: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, certificateArn, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update certificate tags: %w", err)
		}
	}
	return nil
}

func (d *ACMCertificateDriver) scheduleReconcile(ctx restate.ObjectContext, state *ACMCertificateState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ACMCertificateDriver) apiForAccount(ctx restate.ObjectContext, account string) (CertificateAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ACMCertificateDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ACM account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func specFromObserved(observed ObservedState) ACMCertificateSpec {
	options := &CertificateOptions{CertificateTransparencyLoggingPreference: normalizeTransparencyPreference(&observed.Options)}
	return ACMCertificateSpec{
		DomainName:              observed.DomainName,
		SubjectAlternativeNames: append([]string(nil), observed.SubjectAlternativeNames...),
		ValidationMethod:        observed.ValidationMethod,
		KeyAlgorithm:            observed.KeyAlgorithm,
		CertificateAuthorityArn: observed.CertificateAuthorityArn,
		Options:                 options,
		Tags:                    filterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ACMCertificateOutputs {
	return ACMCertificateOutputs{
		CertificateArn:       observed.CertificateArn,
		DomainName:           observed.DomainName,
		Status:               observed.Status,
		DNSValidationRecords: append([]DNSValidationRecord(nil), observed.DNSValidationRecords...),
		NotBefore:            observed.NotBefore,
		NotAfter:             observed.NotAfter,
	}
}

func defaultACMCertificateImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
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
		spec.Tags = make(map[string]string)
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

func normalizeDomainName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeSANs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
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
	if !slices.Equal(desired.SubjectAlternativeNames, observed.SubjectAlternativeNames) {
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

func formatManagedKeyConflict(managedKey, certificateArn string) error {
	return fmt.Errorf("ACM certificate name %q in this region is already managed by Praxis (certificateArn: %s); remove the existing resource or use a different metadata.name", managedKey, certificateArn)
}
