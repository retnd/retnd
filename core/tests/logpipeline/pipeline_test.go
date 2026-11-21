//go:build observability_pipeline

package logpipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/snappy"

	"github.com/retnd/retnd/core/internal/obs"
)

const (
	promtailImage = "grafana/promtail:3.2.1"
	alloyImage    = "grafana/alloy:v1.5.1"
)

var documentedLabels = []string{"instance", "job", "level", "schema"}

// stream is one Loki stream as pushed: its label set and its lines.
type stream struct {
	labels map[string]string
	lines  []string
}

// receiver is a minimal Loki push endpoint. It decodes the snappy-framed
// protobuf PushRequest by hand (field 1 of the request is the repeated
// stream; field 1 of a stream is its label string and field 2 its entries;
// field 2 of an entry is the line), so the test adds no module dependency.
type receiver struct {
	mu      sync.Mutex
	streams []stream
	lines   int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	raw, err := snappy.Decode(nil, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range fields(raw, 1) {
		var st stream
		for _, f := range fieldsAll(s) {
			switch f.num {
			case 1:
				st.labels = parseLabels(string(f.data))
			case 2:
				for _, e := range fieldsAll(f.data) {
					if e.num == 2 {
						st.lines = append(st.lines, string(e.data))
						r.lines++
					}
				}
			}
		}
		r.streams = append(r.streams, st)
	}
	w.WriteHeader(http.StatusNoContent)
}

type field struct {
	num  int
	data []byte
}

func fields(b []byte, num int) [][]byte {
	var out [][]byte
	for _, f := range fieldsAll(b) {
		if f.num == num {
			out = append(out, f.data)
		}
	}
	return out
}

// fieldsAll returns the length-delimited fields of b and skips varints.
func fieldsAll(b []byte) []field {
	var out []field
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return out
		}
		b = b[n:]
		switch key & 7 {
		case 0:
			_, n = binary.Uvarint(b)
			if n <= 0 {
				return out
			}
			b = b[n:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return out
			}
			out = append(out, field{num: int(key >> 3), data: b[n : n+int(l)]})
			b = b[n+int(l):]
		default:
			return out
		}
	}
	return out
}

var labelPair = regexp.MustCompile(`(\w+)="((?:[^"\\]|\\.)*)"`)

func parseLabels(s string) map[string]string {
	m := map[string]string{}
	for _, p := range labelPair.FindAllStringSubmatch(s, -1) {
		m[p[1]] = p[2]
	}
	return m
}

// capturedLog writes what the engine writes to stdout: real lines from the
// real obs.Logger, including a marked action with an action_id, a backup
// set, and every level.
func capturedLog(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	l := obs.New(&buf, obs.LevelDebug)
	ctx := obs.WithBackupSet(context.Background(), "alpha")
	l.Startup(ctx, "1.2.3", "deadbeef", "go1.27")
	a := l.Begin(ctx, obs.EventCycleStart, obs.ActionCycle, "cycle started")
	l.Event(ctx, obs.LevelDebug, "probe", "a debug line")
	l.Event(ctx, obs.LevelWarn, "probe", "a warn line")
	a.Failed(ctx, fmt.Errorf("probe failure"), "cycle failed")
	l.Completed(obs.WithBackupSet(context.Background(), "beta"), obs.ResultSuccess, "probe_done", "done")
	path := filepath.Join(t.TempDir(), "engine.log")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// receiverURL starts the receiver and returns a URL a sibling container can
// reach it at. Inside a container (how scripts/observability/log-pipeline.sh
// runs this) it joins the container to a fresh network and uses its address
// there; on a plain host it uses the host gateway.
func receiverURL(t *testing.T, r *receiver) (url, network string) {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	if _, err := os.Stat("/.dockerenv"); err != nil {
		return fmt.Sprintf("http://host.docker.internal:%d", port), ""
	}
	self, _ := os.Hostname()
	network = fmt.Sprintf("o12-pipeline-%d", time.Now().UnixNano())
	docker(t, "network", "create", network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	docker(t, "network", "connect", network, self)
	ip := docker(t, "inspect", "-f", fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network), self)
	return fmt.Sprintf("http://%s:%d", ip, port), network
}

// run executes one image over the captured log with the given config and
// returns what it pushed. The config and the log travel by docker cp, so no
// host path is mounted.
func run(t *testing.T, image, cfgDest string, cfg []byte, logFile string, args ...string) []stream {
	t.Helper()
	r := &receiver{}
	url, network := receiverURL(t, r)

	// docker cp needs the destination's parent to exist, so both travel as
	// a directory copied into the parent of where they belong.
	cfgDir := filepath.Join(t.TempDir(), filepath.Base(filepath.Dir(cfgDest)))
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, filepath.Base(cfgDest)), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(t.TempDir(), "retnd")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "engine.log"), []byte(mustRead(t, logFile)), 0o644); err != nil {
		t.Fatal(err)
	}
	create := []string{"create", "-e", "LOKI_URL=" + url, "-e", "INSTANCE=pipeline-test"}
	if network != "" {
		create = append(create, "--network", network)
	} else {
		create = append(create, "--add-host=host.docker.internal:host-gateway")
	}
	create = append(create, image)
	create = append(create, args...)
	id := docker(t, create...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	docker(t, "cp", cfgDir, id+":"+filepath.Dir(filepath.Dir(cfgDest))+"/")
	docker(t, "cp", logDir, id+":/var/log/")
	docker(t, "start", id)

	want := strings.Count(mustRead(t, logFile), "\n")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := r.lines
		r.mu.Unlock()
		if got >= want {
			break
		}
		time.Sleep(time.Second)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lines < want {
		logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
		t.Fatalf("%s pushed %d of %d lines within the deadline; its output:\n%s", image, r.lines, want, logs)
	}
	return append([]stream(nil), r.streams...)
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// labelSetProblems is the assertion, kept apart from the run so the
// _WouldCatchOne control can hand it a stream it must refuse.
func labelSetProblems(streams []stream) []string {
	var out []string
	if len(streams) == 0 {
		return []string{"no stream was pushed"}
	}
	for _, s := range streams {
		got := make([]string, 0, len(s.labels))
		for k := range s.labels {
			got = append(got, k)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(documentedLabels, ",") {
			out = append(out, fmt.Sprintf("label set %v, want exactly %v", got, documentedLabels))
		}
		if s.labels["job"] != "retnd" || s.labels["schema"] != "1" {
			out = append(out, fmt.Sprintf("job=%q schema=%q, want retnd and 1", s.labels["job"], s.labels["schema"]))
		}
	}
	return out
}

// everyFieldSurvives checks the other half of the rule: what is not a label
// is still on the line, so it can be parsed as a field at query time.
func everyFieldSurvives(streams []stream) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range streams {
		for _, l := range s.lines {
			for _, k := range []string{`"event":`, `"action_id":`, `"backup_set":`, `"msg":`} {
				if strings.Contains(l, k) {
					seen[k] = true
				}
			}
		}
	}
	for _, k := range []string{`"event":`, `"action_id":`, `"backup_set":`, `"msg":`} {
		if !seen[k] {
			out = append(out, k+" is on no pushed line: it was removed from the line instead of being left as a field")
		}
	}
	return out
}

type pipeline struct {
	name, image, dest, source string
	args                      []string
	// promote plants action_id as a label in this config.
	promote func(cfg string) string
}

var pipelines = []pipeline{
	{
		name: "promtail", image: promtailImage, dest: "/etc/promtail/promtail.yaml",
		source: "../../../container/observability/promtail/promtail.yaml",
		args:   []string{"-config.file=/etc/promtail/promtail.yaml", "-config.expand-env=true"},
		promote: func(cfg string) string {
			cfg = strings.Replace(cfg, "            ts: time\n", "            ts: time\n            action_id: action_id\n", 1)
			return strings.Replace(cfg, "          level:\n", "          level:\n          action_id:\n", 1)
		},
	},
	{
		name: "alloy", image: alloyImage, dest: "/etc/alloy/config.alloy",
		source: "../../../container/observability/alloy/config.alloy",
		args:   []string{"run", "/etc/alloy/config.alloy", "--server.http.listen-addr=0.0.0.0:12345", "--storage.path=/tmp/alloy"},
		promote: func(cfg string) string {
			cfg = strings.Replace(cfg, `      ts    = "time",`, `      ts    = "time",`+"\n"+`      action_id = "action_id",`, 1)
			return strings.Replace(cfg, `      level = "",`, `      level = "",`+"\n"+`      action_id = "",`, 1)
		},
	},
}

func TestShippedPipelinesLabelExactlyTheDocumentedSet(t *testing.T) {
	logFile := capturedLog(t)
	for _, p := range pipelines {
		t.Run(p.name, func(t *testing.T) {
			cfg := []byte(mustRead(t, p.source))
			streams := run(t, p.image, p.dest, cfg, logFile, p.args...)
			if probs := labelSetProblems(streams); len(probs) > 0 {
				t.Errorf("%v\nstreams: %v", probs, streams)
			}
			if probs := everyFieldSurvives(streams); len(probs) > 0 {
				t.Errorf("%v", probs)
			}
		})
	}
}

// _WouldCatchOne: the same run over a config that promotes action_id to a
// label. The assertion must refuse it, and name the label set, or the test
// above is a check that cannot fail.
func TestShippedPipelinesLabelSet_WouldCatchOne(t *testing.T) {
	logFile := capturedLog(t)
	for _, p := range pipelines {
		t.Run(p.name, func(t *testing.T) {
			orig := mustRead(t, p.source)
			planted := p.promote(orig)
			if planted == orig {
				t.Fatal("the planting did not change the config; the control is not planting anything")
			}
			streams := run(t, p.image, p.dest, []byte(planted), logFile, p.args...)
			probs := labelSetProblems(streams)
			if len(probs) == 0 {
				t.Fatalf("a config promoting action_id produced the documented label set; streams: %v", streams)
			}
			if !strings.Contains(strings.Join(probs, " "), "action_id") {
				t.Errorf("the refusal does not name action_id: %v", probs)
			}
		})
	}
}
