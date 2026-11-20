package metrics_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/metrics"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
)

// The dashboard and rules name guard (EPIC O, FR-102, #902, matrix row O-14).
//
// A Grafana panel querying a series this product never emitted draws a flat,
// empty line, and an empty line reads exactly like a healthy one. A Prometheus
// rule over a name nobody emits never fires, which reads exactly like nothing
// being wrong. Neither failure raises anything anywhere, so this is the one
// place either can be caught.
//
// What it checks, over every dashboard JSON and every rules file shipped in
// container/observability/:
//
//   - every retnd_* identifier is a series one of the two renderers emits
//     (a histogram's _bucket, _sum and _count included), where the emitted set
//     is computed by rendering a fully loaded health.Report and a fully loaded
//     workflow counter set, never typed out;
//   - every label a selector, a grouping clause, a legend or an alert template
//     names is a label one of the referenced series carries, and every value a
//     selector compares a closed-vocabulary label against is a value the
//     renderers can emit (state="Stale" matches nothing, and nothing says so);
//   - every Loki query selects on the stream labels the shipped Promtail
//     pipeline really sets, filters on fields the pinned log schema really
//     carries (obs.ReservedKeys), and names event values that exist;
//   - every panel title says what the panel looks like when nothing is wrong,
//     every panel has a query this guard can check, and every datasource uid a
//     panel names is one the shipped provisioning defines.
//
// scripts/observability/check-dashboard-metrics.sh runs this, and
// scripts/ci-local.sh runs that script. RETND_OBSERVABILITY_DIR points it at
// another copy of the bundle, which is how the falsification runs in
// docs/conformance/epic-o-matrix.md plant a violation without touching the
// tree.

// dashExposition is every series the renderers emit, with every label each one
// carries and every value each label took in the golden rendering.
type dashExposition map[string]map[string]map[string]bool

// dashFreeLabels are the labels whose values are operator-chosen or
// build-chosen strings, so a dashboard comparing them against a literal is
// not something the golden rendering can confirm or refute. Every other label
// is a closed vocabulary and its values are checked.
var dashFreeLabels = map[string]bool{
	"backup_set":     true,
	"binary_version": true,
	"rclone_version": true,
	"version":        true,
	"connection":     true,
}

// dashPromLabels are the labels Prometheus itself attaches to every scraped
// series, so a query may group by them although no renderer writes them.
var dashPromLabels = map[string]bool{"instance": true, "job": true}

// dashGoldenExposition renders the fullest report this repository can build
// and the fullest workflow counter set, and parses the result. It starts from
// labels_test.go's two fixtures, which already exercise every label
// vocabulary, and adds the optional readings those fixtures leave unset, so
// that every family renders samples and its labels can be read off them.
func dashGoldenExposition(t *testing.T) dashExposition {
	t.Helper()

	report := labelledReport(t)

	set, err := model.NewBackupSetID("staging", "archive")
	if err != nil {
		t.Fatalf("NewBackupSetID: %v", err)
	}
	now := report.GeneratedAt
	age := 90 * time.Minute
	free := uint64(1 << 40)
	report.BackupSets = append(report.BackupSets, health.BackupSetHealth{
		Set:                   set,
		State:                 health.Healthy,
		LastSuccessfulPollAt:  &now,
		LastCompletedBackupAt: &now,
		NewestGoodBackupAt:    &now,
		NewestGoodBackupAge:   &age,
		StaleThreshold:        26 * time.Hour,
		FreeBytes:             &free,
		LastRetentionRunAt:    &now,
		Placement: health.PlacementHealth{
			AwayFromHome:          1,
			OldestAwayFromHomeAge: &age,
			OpenMoves:             1,
			OldestOpenMoveAge:     &age,
			FailedMoves:           1,
			OldestFailedMoveAge:   &age,
		},
	})
	report.KeyCustody = health.KeyCustodyHealth{
		Enabled:           true,
		ActiveKeyID:       "golden-key",
		UnconfirmedKeyIDs: []string{"golden-key"},
		// An id no suite has, so the "unknown" sample renders too.
		EncryptedCopiesBySuite: map[string]int{"golden-unregistered-suite": 1},
	}

	text := metrics.Render(report) + labelledWorkflow(t).RenderWorkflow()

	out := dashExposition{}
	for _, m := range typePattern.FindAllStringSubmatch(text, -1) {
		// A family with no sample still names its series, so a HELP and TYPE
		// pair alone is enough to count it as emitted. Its labels are only
		// known from samples, which is why the fixture fills everything.
		name, typ := m[1], m[2]
		if typ == "histogram" {
			for _, s := range []string{"_bucket", "_sum", "_count"} {
				if out[name+s] == nil {
					out[name+s] = map[string]map[string]bool{}
				}
			}
			continue
		}
		if out[name] == nil {
			out[name] = map[string]map[string]bool{}
		}
	}
	for _, m := range samplePattern.FindAllStringSubmatch(text, -1) {
		name, lbls := m[1], m[2]
		if out[name] == nil {
			t.Fatalf("sample %s has no TYPE line, so the golden parse is wrong", name)
		}
		for _, p := range dashLabelPairRe.FindAllStringSubmatch(lbls, -1) {
			if out[name][p[1]] == nil {
				out[name][p[1]] = map[string]bool{}
			}
			out[name][p[1]][p[2]] = true
		}
	}
	if len(out) < 40 {
		t.Fatalf("the golden rendering produced %d series names, which is too few to be both renderers", len(out))
	}
	return out
}

var dashLabelPairRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)

// dashLokiSchema is what a Loki query over the shipped pipeline can select and
// filter on: the stream labels with their known values, the parsed fields,
// and the event names.
type dashLokiSchema struct {
	labels map[string]map[string]bool // label -> known values; nil set means free
	fields map[string]bool
	events map[string]bool
	levels map[string]bool
}

// dashLoadLokiSchema computes the Loki side from the things that define it:
// the labels the shipped Promtail pipeline sets (its static labels and its
// labels stage, minus what it drops), obs.ReservedKeys for the fields, the
// Event* constants in core/internal/obs for the event names, and slog's own
// level words lowercased the way the pipeline's template stage does.
func dashLoadLokiSchema(t *testing.T, dir string) dashLokiSchema {
	t.Helper()

	levels := map[string]bool{}
	for _, l := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		levels[strings.ToLower(l.String())] = true
	}

	var cfg struct {
		ScrapeConfigs []struct {
			StaticConfigs []struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"static_configs"`
			PipelineStages []map[string]any `yaml:"pipeline_stages"`
		} `yaml:"scrape_configs"`
	}
	dashReadYAML(t, filepath.Join(dir, "promtail", "promtail.yaml"), &cfg)

	labels := map[string]map[string]bool{}
	for _, sc := range cfg.ScrapeConfigs {
		for _, st := range sc.StaticConfigs {
			for k, v := range st.Labels {
				if strings.HasPrefix(k, "__") {
					continue
				}
				if strings.Contains(v, "${") {
					labels[k] = nil // set from the environment: free
					continue
				}
				if _, seen := labels[k]; !seen {
					labels[k] = map[string]bool{}
				}
				if labels[k] != nil {
					labels[k][v] = true
				}
			}
		}
		for _, stage := range sc.PipelineStages {
			if l, ok := stage["labels"].(map[string]any); ok {
				for k := range l {
					if k == "level" {
						labels[k] = levels
					} else {
						labels[k] = nil
					}
				}
			}
			if drop, ok := stage["labeldrop"].([]any); ok {
				for _, d := range drop {
					delete(labels, fmt.Sprint(d))
				}
			}
		}
	}
	if len(labels) == 0 {
		t.Fatalf("promtail/promtail.yaml sets no stream label this guard could read")
	}

	fields := map[string]bool{}
	for _, k := range obs.ReservedKeys {
		fields[k] = true
	}

	return dashLokiSchema{labels: labels, fields: fields, events: dashEventNames(t), levels: levels}
}

// dashEventNames reads every Event* string constant out of core/internal/obs,
// the same way obs's own pin test does, so a renamed event turns a dashboard
// filtering on the old name red here.
func dashEventNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range []string{"events.go", "assurance_events.go"} {
		file, err := parser.ParseFile(fset, filepath.Join("..", "obs", f), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, id := range vs.Names {
				if !strings.HasPrefix(id.Name, "Event") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					out[v] = true
				}
			}
			return true
		})
	}
	if len(out) < 50 {
		t.Fatalf("read %d event names out of core/internal/obs, which is too few to be the catalogue", len(out))
	}
	return out
}

func dashReadYAML(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := yaml.Unmarshal(data, into); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
}

// dashBundleDir is the bundle under test: the tree's own, unless
// RETND_OBSERVABILITY_DIR names a copy.
func dashBundleDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("RETND_OBSERVABILITY_DIR"); d != "" {
		return d
	}
	return filepath.Join("..", "..", "..", "container", "observability")
}

// dashChecker holds what a query is checked against.
type dashChecker struct {
	exp     dashExposition
	loki    dashLokiSchema
	jobs    map[string]bool // Prometheus job names the shipped prometheus.yml defines
	sources map[string]string
}

func dashNewChecker(t *testing.T, dir string) *dashChecker {
	t.Helper()

	var prom struct {
		ScrapeConfigs []struct {
			JobName string `yaml:"job_name"`
		} `yaml:"scrape_configs"`
	}
	dashReadYAML(t, filepath.Join(dir, "prometheus", "prometheus.yml"), &prom)
	jobs := map[string]bool{}
	for _, sc := range prom.ScrapeConfigs {
		jobs[sc.JobName] = true
	}

	sources := map[string]string{}
	files, _ := filepath.Glob(filepath.Join(dir, "grafana", "provisioning", "datasources", "*.yml"))
	for _, f := range files {
		var ds struct {
			Datasources []struct {
				UID  string `yaml:"uid"`
				Type string `yaml:"type"`
			} `yaml:"datasources"`
		}
		dashReadYAML(t, f, &ds)
		for _, d := range ds.Datasources {
			sources[d.UID] = d.Type
		}
	}
	if len(sources) == 0 {
		t.Fatalf("grafana/provisioning/datasources defines no datasource")
	}

	return &dashChecker{exp: dashGoldenExposition(t), loki: dashLoadLokiSchema(t, dir), jobs: jobs, sources: sources}
}

var (
	dashRetndIdentRe = regexp.MustCompile(`\bretnd_[A-Za-z0-9_]*`)
	dashUpRe         = regexp.MustCompile(`\bup\b`)
	dashGroupingRe   = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\(([^)]*)\)`)
	dashMatcherRe    = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)"`)
	dashTemplateRe   = regexp.MustCompile(`\{\{\s*(?:\$labels\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)
	dashLabelValues  = regexp.MustCompile(`label_values\(\s*(retnd_[A-Za-z0-9_]*)\s*,\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\)`)
	dashRuleLabelRe  = regexp.MustCompile(`\$labels\.([a-zA-Z_][a-zA-Z0-9_]*)`)
	dashLokiFilterRe = regexp.MustCompile(`\|\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=|>=|<=|>|<)\s*"((?:[^"\\]|\\.)*)"`)
)

// selectorAfter returns the {...} block starting at the first non-space byte
// at or after i, or "" when there is none.
func selectorAfter(expr string, i int) string {
	for i < len(expr) && (expr[i] == ' ' || expr[i] == '\t' || expr[i] == '\n') {
		i++
	}
	if i >= len(expr) || expr[i] != '{' {
		return ""
	}
	inQuote := false
	for j := i + 1; j < len(expr); j++ {
		switch {
		case expr[j] == '\\' && inQuote:
			j++
		case expr[j] == '"':
			inQuote = !inQuote
		case expr[j] == '}' && !inQuote:
			return expr[i+1 : j]
		}
	}
	return ""
}

// checkValue says what is wrong with comparing a label against value with op,
// given the values the label is known to take, or "" when nothing is. A nil
// set means the label is free text and no value can be refuted.
func checkValue(label, op, value string, known map[string]bool) string {
	if known == nil || strings.Contains(value, "$") {
		return ""
	}
	if op == "=~" || op == "!~" {
		re, err := regexp.Compile("^(?:" + value + ")$")
		if err != nil {
			return fmt.Sprintf("%s%s%q is not a valid regular expression: %v", label, op, value, err)
		}
		for v := range known {
			if re.MatchString(v) {
				return ""
			}
		}
		return fmt.Sprintf("%s%s%q matches none of the values %s takes (%s)", label, op, value, label, joinKeys(known))
	}
	if !known[value] {
		return fmt.Sprintf("%s%s%q compares against a value %s never takes (%s)", label, op, value, label, joinKeys(known))
	}
	return ""
}

func joinKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 12 {
		keys = append(keys[:12], "...")
	}
	return strings.Join(keys, ", ")
}

// checkPromQL returns every problem with one PromQL expression, plus the label
// names its referenced series carry (for checking a legend or a template).
func (c *dashChecker) checkPromQL(expr string) (problems []string, carried map[string]bool) {
	carried = map[string]bool{}
	for l := range dashPromLabels {
		carried[l] = true
	}
	checkable := false

	for _, loc := range dashRetndIdentRe.FindAllStringIndex(expr, -1) {
		name := expr[loc[0]:loc[1]]
		checkable = true
		series, ok := c.exp[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is not a series retnd emits", name))
			continue
		}
		for l := range series {
			carried[l] = true
		}
		sel := selectorAfter(expr, loc[1])
		for _, m := range dashMatcherRe.FindAllStringSubmatch(sel, -1) {
			label, op, value := m[1], m[2], m[3]
			if dashPromLabels[label] {
				if label == "job" {
					if p := checkValue(label, op, value, c.jobs); p != "" {
						problems = append(problems, name+": "+p+" (the job names come from prometheus/prometheus.yml)")
					}
				}
				continue
			}
			known, has := series[label]
			if !has {
				problems = append(problems, fmt.Sprintf("%s has no label %q (it carries: %s)", name, label, joinKeys(labelSet(series))))
				continue
			}
			if dashFreeLabels[label] {
				known = nil
			}
			if p := checkValue(label, op, value, known); p != "" {
				problems = append(problems, name+": "+p)
			}
		}
	}

	// up is the one non-retnd series a panel may use: it is Prometheus's own
	// reading of whether the scrape answered, and without it an exporter
	// that is down and one that is healthy draw the same empty panel. It is
	// only meaningful scoped to retnd's job, so it must carry one.
	for _, loc := range dashUpRe.FindAllStringIndex(expr, -1) {
		checkable = true
		sel := selectorAfter(expr, loc[1])
		job := false
		for _, m := range dashMatcherRe.FindAllStringSubmatch(sel, -1) {
			if m[1] != "job" {
				continue
			}
			job = true
			if p := checkValue("job", m[2], m[3], c.jobs); p != "" {
				problems = append(problems, "up: "+p+" (the job names come from prometheus/prometheus.yml)")
			}
		}
		if !job {
			problems = append(problems, "up without a job matcher counts every scrape target, not retnd")
		}
	}

	for _, m := range dashGroupingRe.FindAllStringSubmatch(expr, -1) {
		for _, l := range strings.Split(m[1], ",") {
			l = strings.TrimSpace(l)
			if l != "" && !carried[l] {
				problems = append(problems, fmt.Sprintf("groups by %q, which none of the series in this query carries", l))
			}
		}
	}

	for _, m := range dashLabelValues.FindAllStringSubmatch(expr, -1) {
		if s, ok := c.exp[m[1]]; ok {
			if _, has := s[m[2]]; !has {
				problems = append(problems, fmt.Sprintf("label_values reads %q from %s, which has no such label", m[2], m[1]))
			}
		}
	}

	if !checkable {
		problems = append(problems, "queries nothing this guard can check: no retnd_* series and no up{job=...}")
	}
	return problems, carried
}

func labelSet(series map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for l := range series {
		out[l] = true
	}
	return out
}

// checkLogQL returns every problem with one LogQL expression.
func (c *dashChecker) checkLogQL(expr string) (problems []string) {
	start := strings.Index(expr, "{")
	if start < 0 {
		return []string{"a Loki query with no stream selector"}
	}
	sel := selectorAfter(expr, start)
	if sel == "" {
		return []string{"a Loki stream selector that never closes"}
	}
	job := false
	for _, m := range dashMatcherRe.FindAllStringSubmatch(sel, -1) {
		label, op, value := m[1], m[2], m[3]
		known, has := c.loki.labels[label]
		if !has {
			problems = append(problems, fmt.Sprintf("selects on stream label %q, which the shipped Promtail pipeline never sets (it sets: %s)", label, joinKeys(lokiLabelSet(c.loki.labels))))
			continue
		}
		if label == "job" {
			job = true
		}
		if p := checkValue(label, op, value, known); p != "" {
			problems = append(problems, p)
		}
	}
	if !job {
		problems = append(problems, "a Loki query that does not select job, so it reads every stream, not retnd's")
	}

	rest := expr[start+len(sel)+2:]
	for _, m := range dashLokiFilterRe.FindAllStringSubmatch(rest, -1) {
		field, op, value := m[1], m[2], m[3]
		switch {
		case field == "event":
			if p := checkValue(field, op, value, c.loki.events); p != "" {
				problems = append(problems, p+" (event names come from core/internal/obs)")
			}
		case field == "level":
			if p := checkValue(field, op, value, c.loki.levels); p != "" {
				problems = append(problems, p)
			}
		case c.loki.fields[field]:
		default:
			if _, isLabel := c.loki.labels[field]; !isLabel {
				problems = append(problems, fmt.Sprintf("filters on field %q, which is not one of the pinned log keys (%s)", field, joinKeys(c.loki.fields)))
			}
		}
	}
	return problems
}

func lokiLabelSet(m map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// dashFinding is one problem, located.
type dashFinding struct {
	file, where, problem string
}

func (f dashFinding) String() string {
	return fmt.Sprintf("%s: %s: %s", f.file, f.where, f.problem)
}

// checkDashboard checks one dashboard JSON file.
func (c *dashChecker) checkDashboard(path string) []dashFinding {
	rel := filepath.Base(path)
	var out []dashFinding
	add := func(where, problem string) { out = append(out, dashFinding{rel, where, problem}) }

	data, err := os.ReadFile(path)
	if err != nil {
		add("file", err.Error())
		return out
	}
	var dash map[string]any
	if err := json.Unmarshal(data, &dash); err != nil {
		add("file", "not JSON: "+err.Error())
		return out
	}

	if tmpl, ok := dash["templating"].(map[string]any); ok {
		for _, v := range asList(tmpl["list"]) {
			vm, _ := v.(map[string]any)
			q := vm["query"]
			if qm, ok := q.(map[string]any); ok {
				q = qm["query"]
			}
			qs, _ := q.(string)
			if qs == "" || vm["type"] != "query" {
				continue
			}
			where := fmt.Sprintf("variable %q", vm["name"])
			if p := c.datasourceProblem(vm["datasource"], nil, "prometheus"); p != "" {
				add(where, p)
			}
			problems, _ := c.checkPromQL(qs)
			for _, p := range problems {
				add(where, p)
			}
		}
	}

	panels := 0
	var walk func(list []any)
	walk = func(list []any) {
		for _, p := range list {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			if pm["type"] == "row" {
				walk(asList(pm["panels"]))
				continue
			}
			panels++
			title, _ := pm["title"].(string)
			where := fmt.Sprintf("panel %q", title)
			if !strings.Contains(title, "when nothing is wrong") {
				add(where, "the title does not say what the panel looks like when nothing is wrong, so an empty panel reads as reassuring")
			}
			targets := asList(pm["targets"])
			if len(targets) == 0 {
				add(where, "has no query")
			}
			for _, tg := range targets {
				tm, _ := tg.(map[string]any)
				expr, _ := tm["expr"].(string)
				typ := c.sourceType(tm["datasource"], pm["datasource"])
				if p := c.datasourceProblem(tm["datasource"], pm["datasource"], ""); p != "" {
					add(where, p)
				}
				if expr == "" {
					add(where, "a target with no expr")
					continue
				}
				switch typ {
				case "loki":
					for _, p := range c.checkLogQL(expr) {
						add(where, p)
					}
				default:
					problems, carried := c.checkPromQL(expr)
					for _, p := range problems {
						add(where, p)
					}
					legend, _ := tm["legendFormat"].(string)
					for _, m := range dashTemplateRe.FindAllStringSubmatch(legend, -1) {
						if !carried[m[1]] {
							add(where, fmt.Sprintf("the legend names label %q, which no series in its query carries", m[1]))
						}
					}
				}
			}
		}
	}
	walk(asList(dash["panels"]))
	if panels == 0 {
		add("file", "no panels")
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func dsUID(v any) string {
	m, _ := v.(map[string]any)
	uid, _ := m["uid"].(string)
	return uid
}

func (c *dashChecker) sourceType(own, panel any) string {
	uid := dsUID(own)
	if uid == "" {
		uid = dsUID(panel)
	}
	return c.sources[uid]
}

// datasourceProblem says what is wrong with the datasource a query runs
// against. A uid the provisioning does not define is a panel that renders
// "datasource not found" on a fresh install, or worse, the default source.
func (c *dashChecker) datasourceProblem(own, panel any, want string) string {
	uid := dsUID(own)
	if uid == "" {
		uid = dsUID(panel)
	}
	if uid == "" {
		return "names no datasource uid, so it runs against whatever the default happens to be"
	}
	typ, ok := c.sources[uid]
	if !ok {
		return fmt.Sprintf("names datasource uid %q, which grafana/provisioning/datasources does not define", uid)
	}
	if want != "" && typ != want {
		return fmt.Sprintf("runs against datasource %q of type %s, want %s", uid, typ, want)
	}
	return ""
}

// checkRules checks one Prometheus rules file.
func (c *dashChecker) checkRules(path string) []dashFinding {
	rel := filepath.Base(path)
	var out []dashFinding
	var doc struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Record      string            `yaml:"record"`
				Expr        string            `yaml:"expr"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	data, err := os.ReadFile(path)
	if err == nil {
		err = yaml.Unmarshal(data, &doc)
	}
	if err != nil {
		return []dashFinding{{rel, "file", err.Error()}}
	}
	n := 0
	for _, g := range doc.Groups {
		for _, r := range g.Rules {
			n++
			name := r.Alert
			if name == "" {
				name = r.Record
			}
			where := fmt.Sprintf("rule %q", name)
			problems, carried := c.checkPromQL(r.Expr)
			for _, p := range problems {
				out = append(out, dashFinding{rel, where, p})
			}
			for _, text := range r.Annotations {
				for _, m := range dashRuleLabelRe.FindAllStringSubmatch(text, -1) {
					if !carried[m[1]] {
						out = append(out, dashFinding{rel, where, fmt.Sprintf("an annotation reads $labels.%s, which no series in its expr carries", m[1])})
					}
				}
			}
		}
	}
	if n == 0 {
		out = append(out, dashFinding{rel, "file", "no rules"})
	}
	return out
}

// dashCheckBundle runs every check over a bundle directory.
func dashCheckBundle(t *testing.T, dir string) []dashFinding {
	t.Helper()
	c := dashNewChecker(t, dir)

	var out []dashFinding
	dashboards, _ := filepath.Glob(filepath.Join(dir, "grafana", "dashboards", "*.json"))
	if len(dashboards) != 2 {
		out = append(out, dashFinding{"grafana/dashboards", "directory", fmt.Sprintf("holds %d dashboards; FR-102 ships exactly two, Backup health and Workflow hooks", len(dashboards))})
	}
	for _, d := range dashboards {
		out = append(out, c.checkDashboard(d)...)
	}
	rules, _ := filepath.Glob(filepath.Join(dir, "prometheus", "rules", "*.yml"))
	if len(rules) == 0 {
		out = append(out, dashFinding{"prometheus/rules", "directory", "holds no rules file"})
	}
	for _, r := range rules {
		out = append(out, c.checkRules(r)...)
	}
	return out
}

// TestEveryShippedDashboardAndRuleQueriesWhatRetndEmits_902 is the guard.
func TestEveryShippedDashboardAndRuleQueriesWhatRetndEmits_902(t *testing.T) {
	dir := dashBundleDir(t)
	for _, f := range dashCheckBundle(t, dir) {
		t.Errorf("%s\n\nA dashboard panel or a rule querying something retnd does not emit draws an empty line or never fires, and both read as nothing being wrong (FR-102). Query a series the renderers emit, or remove the panel.", f)
	}
}

// The planted violations. Each copies the real bundle into a temporary
// directory, plants exactly one thing, and requires the guard to name the
// file, the panel or rule, and the identifier. The first case is the control:
// the copy with nothing planted must be clean, or every red below could be the
// guard failing on everything.

func dashCopyBundle(t *testing.T) string {
	t.Helper()
	src := dashBundleDir(t)
	dst := t.TempDir()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying the bundle: %v", err)
	}
	return dst
}

// dashPlantPanel appends one panel to a copied dashboard.
func dashPlantPanel(t *testing.T, dir, file, title, expr string) {
	t.Helper()
	path := filepath.Join(dir, "grafana", "dashboards", file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dash map[string]any
	if err := json.Unmarshal(data, &dash); err != nil {
		t.Fatal(err)
	}
	dash["panels"] = append(asList(dash["panels"]), map[string]any{
		"type":       "timeseries",
		"title":      title,
		"datasource": map[string]any{"type": "prometheus", "uid": "prometheus"},
		"targets":    []any{map[string]any{"refId": "A", "expr": expr, "datasource": map[string]any{"type": "prometheus", "uid": "prometheus"}}},
	})
	out, _ := json.Marshal(dash)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func dashFindingsMention(fs []dashFinding, parts ...string) bool {
	for _, f := range fs {
		s := f.String()
		all := true
		for _, p := range parts {
			if !strings.Contains(s, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func TestTheGuardIsCleanOnAnUnplantedCopy_902(t *testing.T) {
	if fs := dashCheckBundle(t, dashCopyBundle(t)); len(fs) != 0 {
		t.Fatalf("a copy of the bundle with nothing planted already has findings, so no red below proves anything: %v", fs)
	}
}

// PV4: a panel querying retnd_backup_set_stale, a name this product has never
// emitted, must turn the guard red naming the file, the panel and the name.
func TestAPanelQueryingANameRetndNeverEmittedIsRed_902(t *testing.T) {
	dir := dashCopyBundle(t)
	title := "Stale sets (0 when nothing is wrong)"
	dashPlantPanel(t, dir, "backup-health.json", title, `sum(retnd_backup_set_stale)`)
	fs := dashCheckBundle(t, dir)
	if !dashFindingsMention(fs, "backup-health.json", title, "retnd_backup_set_stale") {
		t.Fatalf("PV4: a panel querying retnd_backup_set_stale did not turn the guard red naming the file, the panel and the identifier; findings: %v", fs)
	}
}

// PV4's positive control: the same panel over a real name passes, so the
// guard is not simply failing on every planted panel.
func TestAPanelQueryingARealNamePasses_902(t *testing.T) {
	dir := dashCopyBundle(t)
	dashPlantPanel(t, dir, "backup-health.json", "Newest good backup age (low when nothing is wrong)",
		`max by (backup_set) (retnd_backup_set_newest_good_backup_age_seconds)`)
	if fs := dashCheckBundle(t, dir); len(fs) != 0 {
		t.Fatalf("a panel over retnd_backup_set_newest_good_backup_age_seconds was refused, so the guard fails on real names too: %v", fs)
	}
}

// The rules-file variant: a rule naming a family that is not emitted (here, a
// rename of a real one) never fires, and must fail the same way.
func TestARuleNamingARenamedFamilyIsRed_902(t *testing.T) {
	dir := dashCopyBundle(t)
	path := filepath.Join(dir, "prometheus", "rules", "planted.rules.yml")
	body := "groups:\n  - name: planted\n    rules:\n      - alert: PlantedRenamed\n        expr: retnd_backup_set_failure_count > 0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := dashCheckBundle(t, dir)
	if !dashFindingsMention(fs, "planted.rules.yml", "PlantedRenamed", "retnd_backup_set_failure_count") {
		t.Fatalf("a rule over a renamed family did not turn the guard red naming the file, the rule and the identifier; findings: %v", fs)
	}
}

// The quieter drifts the guard also has to see, one per check.
func TestTheGuardSeesLabelValueLegendAndTitleDrift_902(t *testing.T) {
	cases := []struct {
		name, title, expr, want string
	}{
		{"a label the series does not carry", "By reason (flat when nothing is wrong)", `sum(retnd_backup_set_failures{reason="x"})`, `has no label "reason"`},
		{"a closed-vocabulary value that is never emitted", "Stale (0 when nothing is wrong)", `sum(retnd_backup_set_state{state="Stale"})`, `state="Stale"`},
		{"a regex that matches no emitted value", "Odd (0 when nothing is wrong)", `sum(retnd_workflow_runs_total{status=~"error|crashed"})`, `matches none`},
		{"grouping by a label nobody carries", "Grouped (flat when nothing is wrong)", `sum by (repository) (retnd_backup_set_failures)`, `"repository"`},
		{"up without retnd's job", "Up (1 when nothing is wrong)", `up`, `job matcher`},
		{"a title that does not say what healthy looks like", "Failures", `sum(retnd_backup_set_failures)`, `when nothing is wrong`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := dashCopyBundle(t)
			dashPlantPanel(t, dir, "workflow-hooks.json", tc.title, tc.expr)
			fs := dashCheckBundle(t, dir)
			if !dashFindingsMention(fs, "workflow-hooks.json", tc.want) {
				t.Fatalf("planting %s did not turn the guard red with %q; findings: %v", tc.name, tc.want, fs)
			}
		})
	}
}

// The Loki half, against the pinned log schema.
func TestTheGuardSeesLokiDrift_902(t *testing.T) {
	c := dashNewChecker(t, dashBundleDir(t))
	cases := []struct {
		name, expr, want string
	}{
		{"a promoted label the pipeline never sets", `{job="retnd", action_id="x"}`, `"action_id"`},
		{"an event name that does not exist", `{job="retnd"} | json | event="cycle_finished"`, `cycle_finished`},
		{"a level the logger never writes", `{job="retnd", level="fatal"}`, `level="fatal"`},
		{"a field that is not a pinned key", `{job="retnd"} | json | actionId="x"`, `"actionId"`},
		{"no job selector", `{level="error"}`, `does not select job`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := c.checkLogQL(tc.expr)
			if !strings.Contains(strings.Join(ps, "\n"), tc.want) {
				t.Fatalf("%s: want a problem mentioning %q, got %v", tc.expr, tc.want, ps)
			}
		})
	}
	if ps := c.checkLogQL(`sum(count_over_time({job="retnd", level=~"warn|error"} | json | event=~"workflow_.*" [5m]))`); len(ps) != 0 {
		t.Fatalf("a query over real labels, levels and events was refused: %v", ps)
	}
}
