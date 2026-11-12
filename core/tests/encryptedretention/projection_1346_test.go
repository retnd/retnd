package encryptedretention_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/agenttools"
)

// The projection row of the canary corpus (#1346, UV.16): what a tool result
// is made of after agenttools.Project, held by the same detector the other
// rows use. Written by reading the packages it drives; not run.

// keyListResult is a key-list result as the API's JSON decodes, with the canary
// recipient and identity where a projection that only looked at the top level
// would miss them: three levels down, in a list, in a string that is prose and
// under a key that is not on the denylist at all.
func keyListResult(t *testing.T, recipient, identity string) any {
	t.Helper()
	doc := fmt.Sprintf(`{
		"keys": [{
			"id": "k1",
			"status": {"detail": {"recipient": %q, "notes": ["fine", "exported for %s"]}},
			"history": [{"events": [{"label": %q}]}]
		}]
	}`, recipient, recipient, "identity is "+identity)
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTheProjectionLeavesNoDeniedFieldOrShape(t *testing.T) {
	stamp := strings.ToUpper(hex.EncodeToString([]byte(fmt.Sprint(time.Now().UnixNano()))))
	identity := "AGE-SECRET-KEY-1" + stamp + stamp
	recipient := "age1" + strings.ToLower(strings.NewReplacer("0", "q", "1", "p").Replace(stamp+stamp+stamp))
	materials := []string{identity, recipient}

	result := keyListResult(t, recipient, identity)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// The control: the unprojected result carries the canaries, and the
	// detector sees them. Without this a pass below could be a detector that
	// sees nothing.
	if !carries(raw, materials) {
		t.Fatal("the detector does not see the canaries in the unprojected key list")
	}

	projected, err := json.Marshal(agenttools.Project(result))
	if err != nil {
		t.Fatal(err)
	}
	if carries(projected, materials) {
		t.Errorf("a canary survived the projection: %s", projected)
	}
	if strings.Contains(string(projected), `"recipient"`) {
		t.Errorf("a denied field survived the projection: %s", projected)
	}
	if !strings.Contains(string(projected), `"id":"k1"`) {
		t.Errorf("the projection removed more than it should have: %s", projected)
	}
}
