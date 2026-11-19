# EPIC U's token and bearer rows: U1.1, U1.2, UC-01, UV.1, UV.2, UV.3, UV.7 and
# UV.21 (#1345, #1343). Exec'd by selftest.py with Control and PLANT defined.
#
# Each control plants one mutation from the Falsification cell of the row it
# names, in the product file the matrix cites, and the narrowed test that
# claims the row has to go red naming itself.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.agents.selftest import PLANT, Control

SVC_TOKENS = "core/service/agenttokens.go"
WH_ACCESS = "apps/common/webhost/agentaccess.go"
WH_CSRF = "apps/common/webhost/csrf.go"
DENIED_GO = "core/agenttools/denied.go"
CLI_AGENT = "core/cmd/retnd/agent.go"
WEBHOST = "./webhost/"
NOTE = PLANT.strip()

CONTROLS = [
    Control(
        "the token stored as itself instead of its SHA-256 hash",
        ("U1.1", "UV.1", "UC-01"),
        [(SVC_TOKENS, "\t\tHash:      agentTokenHash(token),",
          "\t\tHash:      token, " + NOTE + " the plaintext is the stored hash")],
        "./service/",
        ["TestATokenIsShownOnceAndStoredAsAHash"],
    ),
    Control(
        "a token's status cached past the request that read it",
        ("U1.1",),
        [(SVC_TOKENS, "func (b *BackupService) AuthenticateAgentToken(",
          "var plantedPrincipalCache = map[string]AgentPrincipal{} " + NOTE + " a decision kept past its request\n\n"
          "func (b *BackupService) AuthenticateAgentToken("),
         (SVC_TOKENS, "\tif len(presented) < agentTokenPrefixLen {\n\t\treturn AgentPrincipal{}, ErrAgentTokenInvalid\n\t}",
          "\tif p, ok := plantedPrincipalCache[presented]; ok {\n\t\treturn p, nil\n\t}\n"
          "\tif len(presented) < agentTokenPrefixLen {\n\t\treturn AgentPrincipal{}, ErrAgentTokenInvalid\n\t}"),
         (SVC_TOKENS, "\treturn AgentPrincipal{TokenID: match.ID, Name: match.Name, Scope: match.Scope}, nil",
          "\tplantedPrincipalCache[presented] = AgentPrincipal{TokenID: match.ID, Name: match.Name, Scope: match.Scope}\n"
          "\treturn AgentPrincipal{TokenID: match.ID, Name: match.Name, Scope: match.Scope}, nil")],
        "./service/",
        ["TestARevokedTokenIsRefusedOnTheNextRequest"],
    ),
    Control(
        "Authenticate accepting a token whose row says revoked",
        ("U1.1", "U1.2", "UV.2"),
        [(SVC_TOKENS, "if match == nil || match.RevokedAt != nil || !at.Before(match.ExpiresAt) {",
          "if match == nil || !at.Before(match.ExpiresAt) { " + NOTE + " a revoked row is accepted")],
        "./service/",
        ["TestARevokedTokenIsRefusedOnTheNextRequest"],
    ),
    Control(
        "GET agent tokens answering with the hash",
        ("UV.1", "UV.21"),
        [(SVC_TOKENS, "\tStatus string `json:\"status\"`\n}",
          "\tStatus string `json:\"status\"`\n\tHash   string `json:\"hash\"` " + NOTE + " the hash rides along\n}"),
         ("apps/common/webhost/agentaccess.go",
          "\tout := apicontract.ListAgentTokensResponse{Tokens: make([]apicontract.AgentToken, 0, len(tokens))}\n\tfor _, t := range tokens {\n\t\tout.Tokens = append(out.Tokens, t.Contract())\n\t}\n\twriteJSON(w, http.StatusOK, out)",
          "\twriteJSON(w, http.StatusOK, map[string]any{\"tokens\": tokens}) " + NOTE + " the service rows are returned as they are")],
        WEBHOST,
        ["TestTheTokenListNeverCarriesTheHashOrMoreThanThePrefix"],
        module="apps/common",
    ),
    Control(
        "requireCSRF skipped for every request",
        ("U1.2", "UV.7"),
        [(WH_CSRF, "if _, ok := agentPrincipalFrom(r.Context()); ok {",
          "if _, ok := agentPrincipalFrom(r.Context()); ok || true { " + NOTE + " every request skips CSRF")],
        WEBHOST,
        ["TestACookieSessionStillNeedsTheCSRFPair", "TestOnlyABearerPrincipalSkipsCSRF"],
        module="apps/common",
    ),
    Control(
        "the recoveries prefix dropped from the bearer deny list",
        ("U1.2",),
        [(DENIED_GO, '\t{"", "/recoveries/*"},\n', "\t" + NOTE + " one denied prefix removed\n")],
        WEBHOST,
        ["TestABearerOnADeniedPrefixIsRefusedUnderEveryScope"],
        module="apps/common",
    ),
    Control(
        "every Bearer header treated as an agent token",
        ("U1.2",),
        [(WH_ACCESS, "\tif !strings.HasPrefix(token, service.AgentTokenPrefix) {",
          "\tif false && !strings.HasPrefix(token, service.AgentTokenPrefix) { " + NOTE + " any Bearer header is ours")],
        WEBHOST,
        ["TestAForeignBearerHeaderIsLeftToTheSession"],
        module="apps/common",
    ),
    Control(
        "the scope check dropped so a read token reaches the change path",
        ("UV.3",),
        [(WH_ACCESS, "\t\tif principal.Scope == service.AgentScopeRead {",
          "\t\tif false && principal.Scope == service.AgentScopeRead { " + NOTE + " the scope check is gone")],
        WEBHOST,
        ["TestAReadTokenStartsNothingAndChangesNothing"],
        module="apps/common",
    ),
    Control(
        "a --token flag added to agent token create",
        ("UC-01",),
        [(CLI_AGENT, '\tfs, cfgPath := newFlagSet("agent token create")\n',
          '\tfs, cfgPath := newFlagSet("agent token create")\n\t_ = fs.String("token", "", "a token on the command line") ' + NOTE + " a secret flag\n")],
        "./cmd/retnd/",
        ["TestNoVerbTakesATokenOnItsCommandLine"],
    ),
]
