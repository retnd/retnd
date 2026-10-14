package service

import "github.com/retnd/retnd/core/apicontract"

// The wire objects behind `retnd storage --json`, `retnd medium backends
// --json` and `retnd medium configure --json` (#1154).
//
// Each is a second mapping of a service type next to the web host's own,
// and the pairs are held together by tests in apps/common/webhost that
// marshal both for the same input and compare them field for field: core may
// not import the web host, so the diff lives on its side of the boundary.
// Arrays are empty rather than null on both sides, because one iterates and
// the other has to be guarded.

// StorageWire is the contract's ListStorageStatusResponse, the body GET
// /system/storage serves: the manager-wide reading and one entry per backup
// set.
func StorageWire(manager ManagerStorage, statuses []StorageStatus) apicontract.ListStorageStatusResponse {
	sets := make([]apicontract.StorageStatus, 0, len(statuses))
	for _, s := range statuses {
		sets = append(sets, apicontract.StorageStatus{
			Available:         s.Available,
			AvailableBytes:    s.AvailableBytes,
			BackupSetID:       s.BackupSetID,
			CriticalFreeBytes: s.CriticalFreeBytes,
			FreeBytes:         s.FreeBytes,
			Level:             s.Level,
			LocalPath:         s.LocalPath,
			TotalBytes:        s.TotalBytes,
			UnavailableReason: string(s.UnavailableReason),
			WarningFreeBytes:  s.WarningFreeBytes,
		})
	}
	return apicontract.ListStorageStatusResponse{
		BackupSets: sets,
		Manager: apicontract.ManagerStorage{
			AvailableBytes:    manager.AvailableBytes,
			BindingConstraint: string(manager.BindingConstraint),
			CapBytes:          manager.CapBytes,
			CatalogBytes:      manager.CatalogBytes,
			CatalogBytesKnown: manager.CatalogBytesKnown,
			CriticalFreeBytes: manager.CriticalFreeBytes,
			Denominator:       string(manager.Denominator),
			FreeBytes:         manager.FreeBytes,
			HeadroomBytes:     manager.HeadroomBytes,
			Known:             manager.Known,
			Level:             manager.Level,
			LimitBytes:        manager.LimitBytes,
			MeasuredPath:      manager.MeasuredPath,
			OtherBytes:        manager.OtherBytes,
			OtherBytesKnown:   manager.OtherBytesKnown,
			TotalBytes:        manager.TotalBytes,
			UnknownReason:     string(manager.UnknownReason),
			UsedBytes:         manager.UsedBytes,
			WarningFreeBytes:  manager.WarningFreeBytes,
		},
	}
}

// BackendsWire is the contract's ListBackendsResponse, the body GET /backends
// serves. It carries SHAPE and never a value, and it names no transport:
// BackendCatalog's RcloneBackend stays off this boundary, as it does on the
// API.
func BackendsWire(c BackendCatalog) apicontract.ListBackendsResponse {
	out := apicontract.ListBackendsResponse{
		Backends:           make([]apicontract.BackendManifest, 0, len(c.Backends)),
		InstanceIDPattern:  c.InstanceIDPattern,
		ReservedInstanceID: c.ReservedInstanceID,
		Unregistered:       make([]apicontract.UnregisteredBackend, 0, len(c.Unregistered)),
	}
	for _, b := range c.Backends {
		m := apicontract.BackendManifest{
			Configurable: b.Configurable,
			Fields:       make([]apicontract.BackendManifestField, 0, len(b.Fields)),
			ID:           b.ID,
			Label:        b.Label,
			Probe:        apicontract.BackendProbe{Steps: make([]apicontract.BackendProbeStep, 0, len(b.Probe))},
			Role:         b.Role,
			Summary:      b.Summary,
		}
		for _, f := range b.Fields {
			field := apicontract.BackendManifestField{
				Help:       f.Help,
				ID:         f.ID,
				Kind:       f.Kind,
				Label:      f.Label,
				Pattern:    f.Pattern,
				Required:   f.Required,
				UnsetMeans: f.UnsetMeans,
			}
			if len(f.Values) > 0 {
				field.Values = make([]apicontract.BackendEnumValue, 0, len(f.Values))
				for _, v := range f.Values {
					field.Values = append(field.Values, apicontract.BackendEnumValue{Label: v.Label, Value: v.Value})
				}
			}
			m.Fields = append(m.Fields, field)
		}
		for _, s := range b.Probe {
			m.Probe.Steps = append(m.Probe.Steps, apicontract.BackendProbeStep{Reason: s.Reason, Run: s.Run, Step: s.Step})
		}
		out.Backends = append(out.Backends, m)
	}
	for _, name := range c.Unregistered {
		out.Unregistered = append(out.Unregistered, apicontract.UnregisteredBackend{Transport: name})
	}
	return out
}

// StorageMediumWire is the contract's StorageMediumSummary, the body PUT
// /storage-mediums/{id}/configuration answers with. There is no field on it
// that could carry a credential, in kind or in material (FR-33).
func StorageMediumWire(m StorageMediumSummary) apicontract.StorageMediumSummary {
	return apicontract.StorageMediumSummary{
		Bucket:               m.Bucket,
		ConnectionUnverified: m.ConnectionUnverified,
		Endpoint:             m.Endpoint,
		ID:                   m.ID,
		IsDefault:            m.IsDefault,
		IsLocal:              m.IsLocal,
		Path:                 m.Path,
		Prefix:               m.Prefix,
		ReadsRequireRestore:  m.ReadsRequireRestore,
		Region:               m.Region,
		StorageClass:         m.StorageClass,
		Type:                 m.Type,
		UploadVerification:   m.UploadVerification,
	}
}

// MediumPreflightWire is the contract's MediumPreflightResponse. Every check
// is carried, in the engine's own order, skipped ones included: a surface
// that dropped them would show a shorter list on a failure than on a success,
// which is the one moment the full list matters most.
func MediumPreflightWire(r MediumPreflight) apicontract.MediumPreflightResponse {
	out := apicontract.MediumPreflightResponse{
		Checks: make([]apicontract.MediumPreflightCheck, 0, len(r.Checks)),
		Medium: r.Medium,
		OK:     r.OK,
	}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, apicontract.MediumPreflightCheck{
			Category: c.Category, Detail: c.Detail, Outcome: c.Outcome, Step: c.Step,
		})
	}
	return out
}
