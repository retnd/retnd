package agenttools_test

import (
	"testing"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/service"
)

// The three words are the token store's own. A scope the catalogue spells
// differently from the store would be a tool no token could ever call.
//
// It sits in the external test package because core/service imports this one
// (the audit trail projects every row through agenttools.Project), so an
// internal test that imported core/service would be an import cycle.
func TestTheScopesAreTheTokenStoresScopes(t *testing.T) {
	for _, pair := range [][2]string{
		{string(agenttools.ScopeRead), service.AgentScopeRead},
		{string(agenttools.ScopeOperate), service.AgentScopeOperate},
		{string(agenttools.ScopeConfigure), service.AgentScopeConfigure},
	} {
		if pair[0] != pair[1] {
			t.Errorf("catalogue scope %q is the store's %q", pair[0], pair[1])
		}
	}
}
