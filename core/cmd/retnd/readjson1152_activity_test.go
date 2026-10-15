package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1152, activity paging: `--before CURSOR` and the next_cursor the feed
// prints. The API had both since #730, and a terminal had neither, so the
// only way past the newest page was to raise --limit and print the whole
// history.

// pageOf reads one page and returns it with the cursor the feed names.
func pageOf(t *testing.T, args ...string) apicontract.ListActivityResponse {
	t.Helper()
	out, code := cliJSON(t, args...)
	if code != exitOK {
		t.Fatalf("%v = %d:\n%s", args, code, out)
	}
	var page apicontract.ListActivityResponse
	strictDecode(t, out, &page)
	return page
}

func eventKey(e apicontract.ActivityEvent) string {
	return e.ArtifactID + "|" + e.From + ">" + e.To + "|" + e.OccurredAt
}

func TestActivityJSON_NamesTheCursorOfAFullPage_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)

	full := pageOf(t, "activity", "--json", "--config", configPath)
	if len(full.Events) < 4 {
		t.Fatalf("a cycle recorded only %d transitions; paging needs more than 3 to be exercised", len(full.Events))
	}
	if full.NextCursor != "" {
		t.Errorf("the whole feed fits one page and still names a cursor %q", full.NextCursor)
	}

	first := pageOf(t, "activity", "--json", "--limit", "2", "--config", configPath)
	if len(first.Events) != 2 {
		t.Fatalf("a page of 2 has %d events", len(first.Events))
	}
	if _, err := strconv.ParseInt(first.NextCursor, 10, 64); err != nil || first.NextCursor == "" {
		t.Errorf("next_cursor = %q, want the cursor the API issues (a row id) on a full page", first.NextCursor)
	}
}

func TestActivityBefore_WalksTheWholeFeedWithoutGapsOrRepeats_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)
	all := pageOf(t, "activity", "--json", "--config", configPath).Events

	var walked []apicontract.ActivityEvent
	cursor := ""
	for pages := 0; pages < len(all)+2; pages++ {
		args := []string{"activity", "--json", "--limit", "2", "--config", configPath}
		if cursor != "" {
			args = append(args, "--before", cursor)
		}
		page := pageOf(t, args...)
		walked = append(walked, page.Events...)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(walked) != len(all) {
		t.Fatalf("paging by 2 returned %d events, the whole feed has %d", len(walked), len(all))
	}
	for i := range all {
		if eventKey(walked[i]) != eventKey(all[i]) {
			t.Errorf("event %d differs between the paged walk and the whole feed:\n%s\n%s", i, eventKey(walked[i]), eventKey(all[i]))
		}
	}
}

func TestActivityBefore_TextFormNamesTheNextPageOnStderr_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)

	var out string
	var code int
	stderr := captureStderr(t, func() { out, code = cliJSON(t, "activity", "--limit", "2", "--config", configPath) })
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr, "--before ") {
		t.Errorf("a full text page does not say how to read the next one; stderr:\n%s", stderr)
	}
	// The table itself is pinned line for line by retnd-tests: the hint is
	// not allowed onto stdout.
	if strings.Contains(out, "--before") {
		t.Errorf("the hint is on stdout:\n%s", out)
	}
}

func TestActivityBefore_RefusesACursorTheFeedDidNotIssue_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)
	// The service reads a garbage cursor as no cursor, which for a script
	// paging in a loop is the newest page again, forever.
	for _, bad := range []string{"abc", "0", "-5", "1.5", ""} {
		var code int
		var out string
		stderr := captureStderr(t, func() {
			out, code = cliJSON(t, "activity", "--json", "--before", bad, "--config", configPath)
		})
		if code != exitUsage {
			t.Errorf("--before %q exited %d, want %d", bad, code, exitUsage)
		}
		if strings.TrimSpace(out) != "" || !strings.Contains(stderr, "--before") {
			t.Errorf("--before %q: stdout=%q stderr names the flag=%v", bad, out, strings.Contains(stderr, "--before"))
		}
	}
}

func TestActivityBefore_IsNotAFollowFlag_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)
	var code int
	stderr := captureStderr(t, func() {
		_, code = cliJSON(t, "activity", "--follow", "--before", "5", "--config", configPath)
	})
	if code != exitUsage || !strings.Contains(stderr, "--before") {
		t.Errorf("exit=%d; --before pages the durable log and --follow streams the live one, so the pair is refused", code)
	}
}

func TestActivityBefore_AFilteredPageNeverSilentlySkipsWhatItCut_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)
	all := pageOf(t, "activity", "--json", "--backup-set", cliSet, "--config", configPath)

	// Cut by --limit: the feed cannot name a cursor at the cut (the filter
	// runs after the read), and naming the window's end would skip the rows
	// in between, so it names none and says why.
	var page apicontract.ListActivityResponse
	stderr := captureStderr(t, func() {
		page = pageOf(t, "activity", "--json", "--backup-set", cliSet, "--limit", "2", "--config", configPath)
	})
	if len(page.Events) != 2 {
		t.Fatalf("limit 2 returned %d events", len(page.Events))
	}
	if page.NextCursor != "" {
		t.Errorf("a filtered page that cut %d matching events named cursor %q, which would skip them", len(all.Events)-2, page.NextCursor)
	}
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("a cut filtered page does not say why there is no cursor; stderr:\n%s", stderr)
	}
}

func TestActivityBefore_RoutesThroughAServingEngine_1152(t *testing.T) {
	configPath := oneCycleDeployment(t)
	e := startFakeEngine(t, configPath)
	e.attach(t)

	first := pageOf(t, "activity", "--json", "--limit", "2", "--config", configPath)
	if first.NextCursor == "" || len(first.Events) != 2 {
		t.Fatalf("engine page 1 = %+v", first)
	}
	second := pageOf(t, "activity", "--json", "--limit", "2", "--before", first.NextCursor, "--config", configPath)
	if len(second.Events) == 0 {
		t.Fatalf("engine page 2 is empty")
	}
	if eventKey(second.Events[0]) == eventKey(first.Events[0]) {
		t.Errorf("--before was not sent to the engine: page 2 starts where page 1 did")
	}
	sawEngine := false
	for _, r := range e.requests() {
		if r == "GET /activity" {
			sawEngine = true
		}
	}
	if !sawEngine {
		t.Errorf("the engine was never asked: %v", e.requests())
	}
}
