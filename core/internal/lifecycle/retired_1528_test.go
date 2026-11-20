package lifecycle

import "testing"

// Issue #1528's state. RETIRED is reached only from FAILED and has exactly one
// way out, back to DISCOVERED, which discovery takes when the object a
// retirement called gone is listed again. See
// docs/design/1528-retire-failed-artifacts.md.
//
// The name is spelled as a string here on purpose: the journal stores exactly
// this string, and the state has to exist under exactly this name.
const retiredName = State("RETIRED")

func TestRetiredIsAStateTheMachineKnows_1528(t *testing.T) {
	if _, err := ParseState("RETIRED"); err != nil {
		t.Fatalf("ParseState(RETIRED): %v", err)
	}
}

func TestRetiredIsReachableOnlyFromFailed_1528(t *testing.T) {
	preds := Predecessors(retiredName)
	if len(preds) != 1 || preds[0] != Failed {
		t.Fatalf("Predecessors(RETIRED) = %v, want exactly [FAILED]", preds)
	}
	if err := Validate(Failed, retiredName); err != nil {
		t.Fatalf("Validate(FAILED, RETIRED): %v", err)
	}
	for _, from := range AllStates {
		if from == Failed || from == retiredName {
			continue
		}
		if err := Validate(from, retiredName); err == nil {
			t.Errorf("Validate(%s, RETIRED) is allowed; only a failure can be retired", from)
		}
	}
}

func TestRetiredLeavesOnlyForDiscovered_1528(t *testing.T) {
	succ := Successors(retiredName)
	if len(succ) != 1 || succ[0] != Discovered {
		t.Fatalf("Successors(RETIRED) = %v, want exactly [DISCOVERED]", succ)
	}
}

func TestRetiredIsNotExceptionalAndHoldsNoCopy_1528(t *testing.T) {
	if IsExceptionalState(retiredName) {
		t.Error("RETIRED is exceptional: it is the end of a failure, and logging it as a new one is what keeps a set red")
	}
	if IsDurableRestorePoint(retiredName) {
		t.Error("RETIRED counts as a restore point, but a retired failure never produced a copy")
	}
	if HoldsLocalCopy(retiredName) {
		t.Error("RETIRED claims a local copy; its leftover .partial is removed with the retirement")
	}
	if IsQuarantineState(retiredName) {
		t.Error("RETIRED is a quarantine state; nothing about it waits for a human")
	}
}
