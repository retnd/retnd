package metrics_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// #1595: the report-age panel and alert say what they assume about the
// report cache instead of silently assuming the 15s default.

func TestTheReportAgePanelDoesNotPromiseAMinute_1595(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(dashBundleDir(t), "grafana", "dashboards", "backup-health.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "under a minute") || strings.Contains(s, "well under a minute") {
		t.Error("the report-age panel still promises under a minute, which stops being true above about 45s of min_rebuild_interval")
	}
	if !strings.Contains(s, "when nothing is wrong") || !strings.Contains(s, "rebuild interval") {
		t.Error("the panel title must tie healthy to the rebuild interval and keep saying what nothing wrong looks like")
	}
}

func TestTheReportAgeAlertStatesItsCacheAssumption_1595(t *testing.T) {
	var rules struct {
		Groups []struct {
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	data, err := os.ReadFile(filepath.Join(dashBundleDir(t), "prometheus", "rules", "retnd.rules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	for _, g := range rules.Groups {
		for _, r := range g.Rules {
			if r.Alert != "RetndHealthReportNotRefreshing" {
				continue
			}
			d := r.Annotations["description"]
			if !strings.Contains(d, "min_rebuild_interval") || !strings.Contains(d, "2m") {
				t.Errorf("the description must name min_rebuild_interval and its 2m ceiling, got: %q", d)
			}
			return
		}
	}
	t.Fatal("RetndHealthReportNotRefreshing not found")
}
