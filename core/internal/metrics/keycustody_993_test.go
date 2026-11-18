package metrics

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/health"
)

// #993: EPIC N's key-custody gauges carry no label at all, so no key id can
// become a series, and a deployment that encrypts nothing scrapes exactly as
// it did before.

func keyCustodyReport(h health.KeyCustodyHealth) health.Report {
	r := health.NewReport(health.ProcessHealth{}, nil, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	r.KeyCustody = h
	return r
}

// labelValuesIn is every label value on every sample line, which is where a
// key id would have to be to become a series.
var labelPair = regexp.MustCompile(`(\w+)="([^"]*)"`)

func labelValuesIn(out string) []string {
	var vals []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "{") {
			continue
		}
		for _, m := range labelPair.FindAllStringSubmatch(line, -1) {
			vals = append(vals, m[1]+"="+m[2])
		}
	}
	return vals
}

func TestADeploymentThatEncryptsNothingScrapesNoKeyCustodyFamily_993(t *testing.T) {
	out := Render(keyCustodyReport(health.KeyCustodyHealth{}))
	if strings.Contains(out, "retained_copy") {
		t.Errorf("a deployment with no encryption and no key emits an encryption family:\n%s", out)
	}
}

func TestTheKeyCustodyGaugesHaveNoLabelAndNameNoKey_993(t *testing.T) {
	const activeID, oldID = "k0123456789abcdef", "kfedcba9876543210"
	out := Render(keyCustodyReport(health.KeyCustodyHealth{
		Enabled: true, ActiveKeyID: activeID, UnconfirmedKeyIDs: []string{activeID, oldID},
	}))
	for _, want := range []string{
		"retnd_retained_copy_encryption_enabled 1",
		"retnd_retained_copy_encryption_degraded 1",
		"retnd_retained_copy_key_bundle_confirmed 0",
		"retnd_retained_copy_keys_unconfirmed 2",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("the scrape lacks %q:\n%s", want, out)
		}
	}
	for _, id := range []string{activeID, oldID} {
		if strings.Contains(out, id) {
			t.Errorf("a key id is on the scrape: %s", id)
		}
	}
	for _, kv := range labelValuesIn(out) {
		if strings.Contains(strings.ToLower(kv), "key") {
			t.Errorf("a label names or carries a key: %s", kv)
		}
	}
}

func TestAPlantedKeyIdLabelIsFoundByTheScan_993(t *testing.T) {
	planted := Render(keyCustodyReport(health.KeyCustodyHealth{Enabled: true})) +
		"retnd_retained_copy_key_bundle_confirmed{key_id=\"k0123456789abcdef\"} 1\n"
	found := false
	for _, kv := range labelValuesIn(planted) {
		if strings.Contains(strings.ToLower(kv), "key") {
			found = true
		}
	}
	if !found {
		t.Error("a key id planted as a label value was not found, so the scan above proves nothing")
	}
}

func TestAConfirmedBundleReadsAsOne_993(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	out := Render(keyCustodyReport(health.KeyCustodyHealth{Enabled: true, ActiveKeyID: "k1", BundleConfirmed: true, BundleConfirmedAt: &at}))
	for _, want := range []string{"retnd_retained_copy_encryption_degraded 0", "retnd_retained_copy_key_bundle_confirmed 1", "retnd_retained_copy_keys_unconfirmed 0"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("the scrape lacks %q:\n%s", want, out)
		}
	}
}
