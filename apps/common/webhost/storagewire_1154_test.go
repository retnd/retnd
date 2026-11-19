package webhost

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// #1154: `retnd storage --json`, `retnd medium backends --json` and
// `retnd medium configure --json` print what core/service builds, and this
// file holds each of those to the body the API serves. core may not import
// this package, so the diff lives here: the handler's own bytes and the
// CLI's mapping are decoded to maps and compared field for field, including
// which optional fields are absent.

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return m
}

func TestIssue1154_TheCLIsStorageWireIsTheHandlersBodyFieldForField(t *testing.T) {
	manager := service.ManagerStorage{
		Known: true, MeasuredPath: "/data/backups", TotalBytes: 1_000_000, FreeBytes: 600_000, AvailableBytes: 550_000,
		CatalogBytes: 300_000, CatalogBytesKnown: true, OtherBytes: 100_000, OtherBytesKnown: true,
		CapBytes: 400_000, Denominator: "cap", LimitBytes: 400_000, UsedBytes: 300_000, HeadroomBytes: 100_000,
		BindingConstraint: "cap", WarningFreeBytes: 150_000, CriticalFreeBytes: 50_000, Level: "WARNING",
	}
	statuses := []service.StorageStatus{
		{BackupSetID: "alpha/one", LocalPath: "/data/alpha", Available: true, TotalBytes: 1_000_000, FreeBytes: 500_000,
			AvailableBytes: 450_000, WarningFreeBytes: 100_000, CriticalFreeBytes: 20_000, Level: "OK"},
		{BackupSetID: "alpha/fresh", LocalPath: "/data/nope", Available: false, UnavailableReason: service.StorageUnavailableNotCreated},
	}
	for name, tc := range map[string]struct {
		manager  service.ManagerStorage
		statuses []service.StorageStatus
	}{
		"every field set":                 {manager, statuses},
		"nothing known and no backup set": {service.ManagerStorage{UnknownReason: service.StorageUnknownNoBackupRoot}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newStorageFakeBackend(tc.statuses...)
			backend.manager = tc.manager
			rec := doGet(t, newTestRouterForStorage(t, backend), "/api/v1/system/storage")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			api := decodeMap(t, rec.Body.Bytes())
			cliBytes, err := json.Marshal(service.StorageWire(tc.manager, tc.statuses))
			if err != nil {
				t.Fatal(err)
			}
			if cli := decodeMap(t, cliBytes); !reflect.DeepEqual(api, cli) {
				t.Errorf("the CLI's --json differs from the API body\napi: %v\ncli: %v", api, cli)
			}
			if len(api["manager"].(map[string]any)) < 15 {
				t.Errorf("the API manager block has too few keys to make this comparison mean anything: %v", api["manager"])
			}
		})
	}
}

func TestIssue1154_TheCLIsBackendsWireIsTheHandlersBodyFieldForField(t *testing.T) {
	rec := getBackends(t, newBackupSetsTestRouter(t).router)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	catalog, err := service.RegisteredBackends()
	if err != nil {
		t.Fatal(err)
	}
	cliBytes, err := json.Marshal(service.BackendsWire(catalog))
	if err != nil {
		t.Fatal(err)
	}
	api, cli := decodeMap(t, rec.Body.Bytes()), decodeMap(t, cliBytes)
	if !reflect.DeepEqual(api, cli) {
		t.Errorf("the CLI's --json differs from the API body\napi: %v\ncli: %v", api, cli)
	}
	if backends, _ := api["backends"].([]any); len(backends) < 2 {
		t.Errorf("the API served %d backends, too few to make this comparison mean anything", len(backends))
	}
}

func TestIssue1154_TheCLIsMediumWireIsTheHandlersBodyFieldForField(t *testing.T) {
	for name, m := range map[string]service.StorageMediumSummary{
		"a bucket, every field set": {
			ID: "cold", Type: "s3", Bucket: "b", Region: "eu", Endpoint: "https://e", Prefix: "p", StorageClass: "GLACIER",
			UploadVerification: "attested", ReadsRequireRestore: true, IsDefault: true, ConnectionUnverified: true,
		},
		"a local volume":  {ID: "usb", Type: "local_volume", Path: "/mnt/usb", IsLocal: false, StorageClass: "", UploadVerification: "readback"},
		"the local drive": {ID: "local", Type: "local", Path: "/data", IsLocal: true, IsDefault: true},
	} {
		t.Run(name, func(t *testing.T) {
			api, cli := asMap(t, toStorageMediumBody(m)), asMap(t, service.StorageMediumWire(m))
			if !reflect.DeepEqual(api, cli) {
				t.Errorf("the CLI's --json differs from the API body\napi: %v\ncli: %v", api, cli)
			}
			if len(api) < 6 {
				t.Errorf("the API body has only %d keys; the comparison would pass on two empty objects", len(api))
			}
		})
	}
}

func TestIssue1154_TheCLIsPreflightWireIsTheHandlersBodyFieldForField(t *testing.T) {
	report := service.MediumPreflight{Medium: "usb", OK: false, Checks: []service.MediumPreflightCheck{
		{Step: "reach", Outcome: "passed", Detail: "the directory is there"},
		{Step: "write", Outcome: "failed", Category: "permission", Detail: "cannot write"},
		{Step: "delete", Outcome: "skipped", Detail: "nothing was written"},
	}}
	api, cli := asMap(t, toMediumPreflightResponse(report)), asMap(t, service.MediumPreflightWire(report))
	if !reflect.DeepEqual(api, cli) {
		t.Errorf("the CLI's --json differs from the API body\napi: %v\ncli: %v", api, cli)
	}
	if got := asMap(t, service.MediumPreflightWire(service.MediumPreflight{Medium: "usb", OK: true})); !reflect.DeepEqual(got["checks"], []any{}) {
		t.Errorf("a report with no checks is %v, want an empty array and not null", got["checks"])
	}
}
