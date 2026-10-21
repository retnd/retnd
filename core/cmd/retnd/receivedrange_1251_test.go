package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1251. The Backups page narrows its list to the backups received in
// a date range ("Received on or after", "Received on or before"), inclusive
// on both ends and by calendar day. `retnd artifacts` had no date bounds at
// all and `retnd recover --set` had a lower one only, so the terminal could
// not say the same thing without piping JSON through jq. Both verbs take
// --since DATE and --until DATE now, read by the one parser `recover
// --since` already used: a bare date is that whole day, so --until
// 2026-03-15 keeps a backup received at 23:30 that day, and an RFC 3339 time
// is that moment exactly.
//
// The fixture is three backups of one set whose arrival time (the journal's
// discovered_at, the field the API serves as discovered_at) is set by hand
// after a real cycle, so each bound has a row on either side of it. Their
// updated_at is set to one later moment for all three, the way a set flipped
// read-only and back leaves it: the review of #1256 found the range measured
// on updated_at, which moves on every transition, so a March backup touched
// in October read as received in October.

// rangeTouched is when every fixture backup last changed state, after all
// three arrived: a range read off updated_at sees all three here.
const rangeTouched = "2026-10-20T12:00:00Z"

const (
	rangeOlder  = "older.dump"  // 2026-03-01T10:00:00Z
	rangeMiddle = "backup.dump" // 2026-03-15T23:30:00Z, late on the day an --until names
	rangeNewer  = "newer.dump"  // 2026-04-02T08:00:00Z
)

func receivedRangeDeployment(t *testing.T) string {
	t.Helper()
	configPath := writeTestConfig(t)
	remote := filepath.Join(filepath.Dir(configPath), "remote")
	writeFileT(t, filepath.Join(remote, rangeOlder), "older payload")
	writeFileT(t, filepath.Join(remote, rangeNewer), "newer payload")
	if got := run([]string{"run", "--config", configPath}); got != 0 {
		t.Fatalf("run = %d, want 0 (this test needs three committed backups)", got)
	}
	db, err := sql.Open("sqlite", journalNamedByTestConfig(t, configPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for name, at := range map[string]string{
		rangeOlder:  "2026-03-01T10:00:00Z",
		rangeMiddle: "2026-03-15T23:30:00Z",
		rangeNewer:  "2026-04-02T08:00:00Z",
	} {
		res, err := db.ExecContext(context.Background(), `UPDATE artifacts SET discovered_at = ?, updated_at = ? WHERE artifact_name = ?`, at, rangeTouched, name)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("fixture: %s is not one journal row (%d)", name, n)
		}
	}
	return configPath
}

// listedNames is the file names a text listing printed, in order.
func listedNames(out string) []string {
	var names []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || !strings.HasPrefix(f[0], "production/postgres-primary/") {
			continue
		}
		names = append(names, strings.TrimPrefix(f[0], "production/postgres-primary/"))
	}
	sort.Strings(names)
	return names
}

func sorted(names ...string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

func sameNames(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(sorted(want...), ",") {
		t.Errorf("%s = %v, want %v", what, got, sorted(want...))
	}
}

func TestIssue1251_ArtifactsSinceAndUntilNarrowTheList(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"no bounds", nil, []string{rangeOlder, rangeMiddle, rangeNewer}},
		{"since a date", []string{"--since", "2026-03-02"}, []string{rangeMiddle, rangeNewer}},
		{"since the day itself", []string{"--since", "2026-03-15"}, []string{rangeMiddle, rangeNewer}},
		{"until a date keeps the whole of that day", []string{"--until", "2026-03-15"}, []string{rangeOlder, rangeMiddle}},
		{"one day", []string{"--since", "2026-03-15", "--until", "2026-03-15"}, []string{rangeMiddle}},
		{"since an RFC 3339 time is at or after it", []string{"--since", "2026-03-15T23:30:00Z"}, []string{rangeMiddle, rangeNewer}},
		{"until an RFC 3339 time is that moment, not the day", []string{"--until", "2026-03-15T23:00:00Z"}, []string{rangeOlder}},
		{"until an RFC 3339 time includes the moment", []string{"--until", "2026-03-15T23:30:00Z"}, []string{rangeOlder, rangeMiddle}},
		{"an offset is honoured", []string{"--until", "2026-03-16T00:30:00+01:00"}, []string{rangeOlder, rangeMiddle}},
		{"a range with nothing in it", []string{"--since", "2026-03-20", "--until", "2026-03-25"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"artifacts", "--config", configPath}, tc.args...)
			out := artifactsOut(t, 0, args...)
			sameNames(t, "listed", listedNames(out), tc.want)
			if want := len(tc.want); !strings.Contains(out, "\n"+strconv.Itoa(want)+" artifact(s)") && !strings.HasPrefix(out, strconv.Itoa(want)+" artifact(s)") {
				t.Errorf("the footer does not count %d:\n%s", want, out)
			}
		})
	}
}

func TestIssue1251_ArtifactsRangeNarrowsTheJSONList(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	out := artifactsOut(t, 0, "artifacts", "--json", "--since", "2026-03-02", "--until", "2026-03-15", "--config", configPath)
	var got apicontract.ListArtifactsResponse
	strictDecode(t, out, &got)
	var names []string
	for _, a := range got.Artifacts {
		names = append(names, strings.TrimPrefix(a.ID, "production/postgres-primary/"))
	}
	sort.Strings(names)
	sameNames(t, "--json listed", names, []string{rangeMiddle})
}

func TestIssue1251_ArtifactsRangeNarrowsTheCount(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	if out := artifactsOut(t, 0, "artifacts", "--count", "--until", "2026-03-15", "--config", configPath); strings.TrimSpace(out) != "2" {
		t.Errorf("artifacts --count --until 2026-03-15 = %q, want 2", out)
	}
	if out := artifactsOut(t, 0, "artifacts", "--count", "--json", "--since", "2026-04-01", "--config", configPath); strings.TrimSpace(out) != `{"count":1}` && !strings.Contains(out, `"count": 1`) {
		t.Errorf("artifacts --count --json --since 2026-04-01 = %q, want a count of 1", out)
	}
	// With --state the range applies as well: the state every row is in,
	// read off the list, counted over one day of it.
	st := strings.Fields(strings.Split(artifactsOut(t, 0, "artifacts", "--config", configPath), "\n")[0])[1]
	if out := artifactsOut(t, 0, "artifacts", "--count", "--state", st, "--since", "2026-03-02", "--until", "2026-03-15", "--config", configPath); strings.TrimSpace(out) != "1" {
		t.Errorf("artifacts --count --state %s with a one-backup range = %q, want 1", st, out)
	}
}

func TestIssue1251_ArtifactsRangeIsRefusedWhenItCannotMeanAnything(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"until before since", []string{"--since", "2026-03-15", "--until", "2026-03-14"}, "--until 2026-03-14 is before --since 2026-03-15"},
		{"until before since as times", []string{"--since", "2026-03-15T12:00:00Z", "--until", "2026-03-15T11:00:00Z"}, "is before --since"},
		{"until before since, json", []string{"--json", "--since", "2026-03-15", "--until", "2026-03-14"}, "is before --since"},
		{"until before since, count", []string{"--count", "--since", "2026-03-15", "--until", "2026-03-14"}, "is before --since"},
		{"a since that is not a date", []string{"--since", "yesterday"}, "--since"},
		{"an until that is not a date", []string{"--until", "tomorrow"}, `--until "tomorrow" is not a date`},
		{"a range with one artifact", []string{"--since", "2026-03-01", recoverID}, "--since/--until"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkExitAndStderr(t, append([]string{"artifacts", "--config", configPath}, tc.args...), exitUsage, tc.says)
		})
	}
}

func TestIssue1251_ArtifactsUsageOffersTheRange(t *testing.T) {
	text := captureStderr(t, usage)
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "  artifacts ") && strings.Contains(l, "--since") {
			line = l
		}
	}
	if !strings.Contains(line, "[--since DATE]") || !strings.Contains(line, "[--until DATE]") {
		t.Errorf("usage() has no artifacts synopsis offering [--since DATE] [--until DATE]; found %q", line)
	}
	block := usageBlock(t, text, line, "  artifacts <source/backup-set/name>", "  activity ")
	if !strings.Contains(block, "inclusive") || !strings.Contains(block, "RFC 3339") {
		t.Errorf("the artifacts range entry does not say the bounds are inclusive and what a DATE may be:\n%s", block)
	}
}

// planNames runs a recover --set pre-flight and returns the files it planned.
func planNames(t *testing.T, configPath string, extra ...string) []string {
	t.Helper()
	args := append([]string{"recover", "--config", configPath, "--set", recoverSetID, "--to", "restored", "--preflight", "--json"}, extra...)
	var code int
	out := captureStdout(t, func() { code = run(args) })
	if code != 0 {
		t.Fatalf("run(%v) = %d, output %q", args, code, out)
	}
	var plan struct {
		Items []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("the pre-flight is not JSON: %v\n%s", err, out)
	}
	var names []string
	for _, it := range plan.Items {
		names = append(names, strings.TrimPrefix(it.ArtifactID, recoverSetID+"/"))
	}
	sort.Strings(names)
	return names
}

func TestIssue1251_RecoverSetUntilNarrowsTheSet(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"the whole set", nil, []string{rangeOlder, rangeMiddle, rangeNewer}},
		{"until a date keeps the whole of that day", []string{"--until", "2026-03-15"}, []string{rangeOlder, rangeMiddle}},
		{"until an RFC 3339 time is that moment", []string{"--until", "2026-03-15T23:00:00Z"}, []string{rangeOlder}},
		{"since and until together", []string{"--since", "2026-03-02", "--until", "2026-03-15"}, []string{rangeMiddle}},
		{"newest-only picks the newest inside the range", []string{"--newest-only", "--until", "2026-03-15"}, []string{rangeMiddle}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sameNames(t, "planned", planNames(t, configPath, tc.args...), tc.want)
		})
	}
}

func TestIssue1251_RecoverSetUntilPlacesOnlyTheRange(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", recoverSetID, "--since", "2026-03-02", "--until", "2026-03-15", "--to", "restored", "--json"})
	})
	if code != 0 {
		t.Fatalf("recover --set --since --until = %d, output %q", code, out)
	}
	var res struct {
		Totals struct {
			Recovered int `json:"recovered"`
		} `json:"totals"`
		Items []struct {
			ArtifactID string `json:"artifact_id"`
			Path       string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json output is not JSON: %q", out)
	}
	if res.Totals.Recovered != 1 || len(res.Items) != 1 || !strings.HasSuffix(res.Items[0].ArtifactID, "/"+rangeMiddle) {
		t.Fatalf("result = %+v, want exactly %s recovered", res, rangeMiddle)
	}
	if _, err := os.Stat(res.Items[0].Path); err != nil {
		t.Fatalf("placed file missing: %v", err)
	}
}

func TestIssue1251_RecoverUntilIsRefusedBeforeAnythingIsOpened(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"until without a set", []string{recoverID, "--until", "2026-03-15", "--to", "out"}, "--until need --set"},
		{"until with several ids", []string{recoverID, recoverID + "2", "--until", "2026-03-15", "--to", "out"}, "--until need --set"},
		{"until before since", []string{"--set", recoverSetID, "--since", "2026-03-15", "--until", "2026-03-14", "--to", "out"}, "--until 2026-03-14 is before --since 2026-03-15"},
		{"an until that is not a date", []string{"--set", recoverSetID, "--until", "tomorrow", "--to", "out"}, `--until "tomorrow" is not a date`},
		{"until with all", []string{"--set", recoverSetID, "--all", "--until", "2026-03-15", "--to", "out"}, "--all is every restore point"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				if code := cmdRecover(tc.args); code != exitUsage {
					t.Errorf("cmdRecover = %d, want %d", code, exitUsage)
				}
			})
			if !strings.Contains(out, tc.says) {
				t.Errorf("stderr %q does not say %q", strings.TrimSpace(out), tc.says)
			}
		})
	}
}

func TestIssue1251_RecoverUsageOffersUntil(t *testing.T) {
	text := captureStderr(t, usage)
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "  recover --set ") {
			line = l
		}
	}
	if !strings.Contains(line, "--until DATE") {
		t.Errorf("the recover --set synopsis does not offer --until DATE: %q", line)
	}
	if !strings.Contains(text, "--newest-only, --since and --until narrow a set") {
		t.Errorf("usage() does not say --until narrows a set beside --newest-only and --since")
	}
}

// TestIssue1251_ATransitionAfterArrivalDoesNotMoveABackupInTheRange flips the
// middle backup through REMOTE_RETAINED and back just now, which is what a set
// going read-only and coming back does, and checks both verbs still place it
// on the day it arrived.
func TestIssue1251_ATransitionAfterArrivalDoesNotMoveABackupInTheRange(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	dbPath := journalNamedByTestConfig(t, configPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var st string
	if err := db.QueryRow(`SELECT state FROM artifacts WHERE artifact_name = ?`, rangeMiddle).Scan(&st); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ctx := context.Background()
	j, err := state.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := app.ParseArtifactID(recoverID)
	if err != nil {
		t.Fatal(err)
	}
	for i, hop := range [][2]string{{st, string(lifecycle.RemoteRetained)}, {string(lifecycle.RemoteRetained), st}} {
		if _, err := j.RecordTransition(ctx, state.Transition{Artifact: id, Key: "flip-" + strconv.Itoa(i), From: hop[0], To: hop[1], OccurredAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()

	out := artifactsOut(t, 0, "artifacts", "--config", configPath, "--since", "2026-03-15", "--until", "2026-03-15")
	sameNames(t, "listed for its arrival day", listedNames(out), []string{rangeMiddle})
	today := time.Now().UTC().Format("2006-01-02")
	out = artifactsOut(t, 0, "artifacts", "--config", configPath, "--since", today)
	sameNames(t, "listed for today, when it was only touched", listedNames(out), nil)
	sameNames(t, "newest-only over the whole set", planNames(t, configPath, "--newest-only"), []string{rangeNewer})
	sameNames(t, "newest-only up to its arrival day", planNames(t, configPath, "--newest-only", "--until", "2026-03-15"), []string{rangeMiddle})
}
