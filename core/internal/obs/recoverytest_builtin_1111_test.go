package obs

import (
	"context"
	"testing"
)

// #1111: a built-in structure pass proves the file decodes, so the feed says
// "recovered and readable" and not "recovery test passed". The state field
// and the stage order do not move.
func TestIssue1111_ABuiltInPassIsWordedRecoveredAndReadable(t *testing.T) {
	l, buf := newRecorder(t)
	l.RecoveryTest(context.Background(), RecoveryTestFacts{
		Artifact: "api-server/files/a.tar.zst", Medium: "local", State: "passed", DigestMatched: "matched",
		ValidatorID: "retnd-builtin-structure", Termination: "exited", Cleanup: "removed",
	})
	line := decodeLines(t, buf)[0]
	if msg, _ := line["msg"].(string); msg != "recovered and readable: api-server/files/a.tar.zst (built-in check)" {
		t.Errorf("message = %q", msg)
	}
	if line["state"] != "passed" || line["result"] != "success" {
		t.Errorf("state/result = %v/%v: the fields must not move", line["state"], line["result"])
	}
}

func TestIssue1111_AValidatorPassAndABuiltInFailureKeepTheirWords(t *testing.T) {
	for name, tc := range map[string]struct{ state, id, want string }{
		"validator pass":  {"passed", "pg-check", "recovery test passed"},
		"no id":           {"passed", "", "recovery test passed"},
		"builtin failure": {"failed", "retnd-builtin-structure", "recovery test failed"},
		"builtin stopped": {"could_not_complete", "", "recovery test did not complete"},
	} {
		l, buf := newRecorder(t)
		l.RecoveryTest(context.Background(), RecoveryTestFacts{Artifact: "a/b/c.gz", State: tc.state, ValidatorID: tc.id, DigestMatched: "matched"})
		if msg, _ := decodeLines(t, buf)[0]["msg"].(string); msg != tc.want {
			t.Errorf("%s: message = %q, want %q", name, msg, tc.want)
		}
	}
}

func TestIssue1111_TheStartLineNamesTheBuiltInCheck(t *testing.T) {
	l, buf := newRecorder(t)
	l.RecoveryTestStart(context.Background(), "op1", "prod/db", "prod/db/a.tar.zst", "retnd-builtin-structure")
	line := decodeLines(t, buf)[0]
	if msg, _ := line["msg"].(string); msg != "recovery test started: prod/db/a.tar.zst (built-in structure check)" {
		t.Errorf("message = %q", msg)
	}
	if line["event"] != EventRecoveryTestStart || line["validator"] != "retnd-builtin-structure" {
		t.Errorf("event/validator = %v/%v", line["event"], line["validator"])
	}
	l, buf = newRecorder(t)
	l.RecoveryTestStart(context.Background(), "op2", "prod/db", "prod/db/a.sql.gz", "pg-check")
	if msg, _ := decodeLines(t, buf)[0]["msg"].(string); msg != "recovery test starting" {
		t.Errorf("a validator run's start line moved: %q", msg)
	}
}
