# ADR 0034: An agent is a scoped client of the API, and nothing it reads can act without a person

- Status: accepted. The code landed in EPIC U phase 1 (#1345 to #1348) and phase 2 (#1349 to
  #1353), which merge together, and #1353 marked this record accepted and filled in the owner
  decisions below.
- Date: 2026-11-11
- Scope: EPIC U (#1343), FR-91 through FR-98. Recorded by U1.1 (#1344), measured on `main` at
  `9cd53e1f`.
- Number: `docs/adr/` ends at `0033`; `0031` and `0032` are EPIC P's in prose; nothing reserves
  `0034`.
- Builds on ADR 0020 (the privilege boundary it does not cross), ADR 0029 (the authorisation
  model it extends with a first principal that carries authority), ADR 0027 and 0028 (the
  key-material rule it applies to a new surface). Supersedes nothing.

## Context

Nothing in this tree can authenticate a caller that is not a person. `/api/v1` knows a session
cookie and a CSRF pair (`core/internal/apiclient/client.go:38-39`); `capabilities.AuthContext`
is `Authenticated`, `Username`, `Mode`, `SessionExpiresAt` and carries no authority
(`apps/common/platform/capabilities/capabilities.go:85-90`); ADR 0029 states the consequence,
that any authenticated caller can do anything. The action log records what changed and not
what was read (`apps/common/webhost/actionlog.go:202`). The destructive gate is shut and stays
shut (`apps/common/webhost/gate.go:111-114`).

An AI agent connecting to this product has to be a caller the tree can name, with less
authority than a person, whose reads are recorded, and whose every attempt to change
something passes through a person. The Model Context Protocol is how agents connect to
tools today; it is a JSON-RPC vocabulary of tools, resources and prompts over stdio or
streamable HTTP, and it says nothing about who the caller is or what they may do. Those two
questions are this record.

## Decision 1: a client of `/api/v1`, never a second implementation

Both transports call `/api/v1` with a bearer token and nothing else. The stdio transport is
a `retnd` verb beside a serving engine, the way every other verb beside an engine already
works (`RETND_API_URL`, `core/internal/apiclient`). The HTTP transport is a route group
mounted inside `/api/v1` at `/agent/mcp`, so `RequestScope`, `authMiddleware`,
`recordActions` (`router.go:349-363`) and the `serve-ui` proxy (`serve/ui.go:182`) apply to it
without a line of new wiring.

The alternatives were a separate process (a second thing to install, supervise and version,
with a socket ADR 0020 would have to defend) and an in-process server calling `core/service`
directly (a second client of the service layer with its own refusal mapping, which is the
drift `docs/epic-checklist.md` section 3 exists to prevent). Neither buys anything a route
under `/api/v1` does not, and both cost a boundary.

Consequence: a tool result carries the API's `code`, `message` and `X-Correlation-Id`, the
`cliecho` echo of the command, and an `exit_code` from `engineRefusalExit`
(`core/cmd/retnd/setup.go:748`). A refusal is an `isError` tool result, never a protocol
error, so the agent sees the sentence an operator would.

## Decision 2: a token is a principal with a scope, and `read` is the only default

An agent token is minted by a signed-in operator, named, hashed at rest (SHA-256, the
`VerificationTokenHash` pattern, `apps/common/auth/local/store.go:89-112`), shown once, with an
owner, an expiry (90 days default, one year ceiling, no non-expiring token), a last-used time
and a revocation checked on every request. The principal is `agent:<name>` wherever `Actor` is
today, so a feed can tell two operators and three agents apart. The owner of a token
minted at the terminal with no session is recorded as `cli`.

The token comes back in the mint response, so over plain HTTP from another machine it is
exposed the way an exported key is. The engine therefore refuses `POST /agent/tokens` there
with `403 INSECURE_TRANSPORT_ACK_REQUIRED` unless the request carries the insecure-transport
acknowledgement, the one key export already asks. It reads the connection and not the
destructive gate, so it holds on a gateway profile too, and `GET /system/capabilities` reports
`insecure_transport_ack_required` from the connection alone (plain HTTP from another machine),
whatever the gate says, so the dialog asks exactly when the engine would refuse. The mint dialog asks the person first
and the engine asks the request, so the browser and a remote `retnd agent token create`
behave the same: the latter exits 52 until `RETND_API_INSECURE_TRANSPORT=acknowledged` is
set. Loopback and HTTPS need nothing. The owner chose enforcement over browser-only consent,
knowing it changes 0.8.0 behaviour for a remote mint.

The store is not in `apps/common/auth/local`: that package is a pattern I copied, not a home.
The table accessors live in `core/internal/state` and the mint, check and revoke in
`core/service`, which keeps `core` free of `apps` and lets the CLI and the HTTP routes share
one implementation. The bearer authenticator itself is in `apps/common/webhost`.

A bearer is an agent token only if it carries the `rat_` prefix (owner decision D10). Any other
`Authorization` header is ignored, so a front proxy that adds its own `Bearer` header is
untouched, a login behind it works as before, and a deployment with no tokens stays
byte-identical.

Scope is a closed vocabulary: `read`, `operate`, `configure`, and the scopes nest: `operate`
includes `read`, and `configure` includes `operate`. `read` is the default and the
only scope anything suggests. A `read` token can start nothing and change nothing. No scope
reaches authentication, first-run, key export, import, decrypt, bundle confirmation, recovery
grants, SSH keys, storage credentials, workflow secrets, catalog rebuild, quarantine
reinstate, retention apply, the creation or deletion of a set or a medium, or token minting.
Those routes are catalogue rows with a stated reason, and a planted tool over one fails a
guard.

On the server-side bearer path the denied prefixes are `/auth`, `/system/first-run`,
`/recoveries` and `/agent/tokens`, refused `403 AGENT_ROUTE_DENIED` before the scope is looked
at, with or without a token store. U2.1's `/agent/confirmations/{id}/approve` and `/deny` get
the same treatment, so an agent cannot approve its own confirmation. U1.3 owns the catalogue's
deny list and has to make the bearer path read it, so the two cannot drift.

Scopes rather than a permission matrix because three words an operator can hold in their head
are what gets configured correctly; the mapping from scope to route is the catalogue's and is
printed by `retnd agent tools`.

## Decision 3: one catalogue, keyed by the routes that exist

`core/agenttools` is a table. A row is a route pattern (the same strings
`core/cliecho/routes.go` uses), and either a tool with a scope and a static description or an
empty tool with a `why`. A guard in the shape of `TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap`
holds every registered route to exactly one row. Both transports read the one table. The
resources (`retnd://...`) and prompts are rows of the same table.

Descriptions, resource descriptions and prompt text are static strings pinned by a golden
test. Nothing a model reads as instruction is ever composed from data, and a description that
interpolates a set name turns the test red.

## Decision 4: bearer skips CSRF, and only bearer

`requireCSRF` refuses a request with no cookie (`apps/common/webhost/csrf.go:50`), which as
written refuses every bearer request to a mutating route. CSRF defends a credential the
browser sends by itself; a token in an `Authorization` header is not one. So the authenticator
marks the principal kind on the request context, `requireCSRF` passes a bearer principal, and
a cookie session keeps the pair. A planted "skip for everyone" turns
`TestACookieSessionStillNeedsTheCSRFPair` red.

A bearer on a mutating route is refused with a status that says why: `403
AGENT_SCOPE_INSUFFICIENT` for a `read` token, `409 AGENT_CONFIRMATION_REQUIRED` for an
`operate` or `configure` one. A bearer request creates a confirmation only when it names a catalogue tool whose method,
route and action match it exactly (U2.1); any other is refused and records nothing, so the
refusal is the whole effect of the request.

The MCP endpoint is bearer-only in the other direction too: a cookie session presented to
`/api/v1/agent/mcp` is refused, so a browser tab can never be an MCP client by accident.

## Decision 5: a mutation is a confirmation, and approval replays a record

A tool call under `operate` or `configure` executes nothing. It writes a confirmation record
(`agent_confirmations`): the request body byte for byte, the echoed `retnd` command, the set,
the token, and an expiry (`agents.confirmation_ttl`, 15 minutes). The agent gets
`AGENT_CONFIRMATION_REQUIRED` with the id and the command. An operator approves with a
step-up (`VerifyStepUp`, #1163) or denies, from the terminal or the browser; approval replays
the stored body through the handler the browser would have hit, with the token as the
recorded actor and the approver in `decided_by`. A body supplied at approval is refused. A
token may have at most `agents.max_pending_confirmations` (5) open.

A `run_cycle` or `run_backup_set` proposal that names no `config_revision` is recorded against the
revision the engine holds when it is proposed, so the agent never has to read one and the record is
still what the person approves; one the agent named is kept. The replay then meets the engine's own
revision check, so a configuration that changed before the approval ends `CONFIG_REVISION_STALE`
(exit 1) instead of running, and the confirmation view shows the revision it was recorded against.
A body at approval is still refused.

The replay meets every refusal the original would have: the shut operation gate answers
`DESTRUCTIVE_OPERATIONS_DISABLED` and exit 50 for `run_cycle` exactly as `retnd run` does.
This epic opens no gate.

The transport rule is judged on the approver's connection, the same as for their own call. The
replay takes the approve request's peer address, TLS state, `X-Forwarded-For`,
`X-Forwarded-Proto`, `X-Retnd-Browser-Transport` and insecure-transport acknowledgement, and
nothing else from it, so a loopback or https approver replays as loopback or https, and a
plain-http approver from another machine still gets `INSECURE_TRANSPORT_ACK_REQUIRED` and exit 52
unless the approval carried the acknowledgement. Forwarded headers count exactly when the
deployment trusts them.

A pre-approved `operate` token, which would skip the confirmation for unattended work, is
owner decision D2 and is not in this epic.

## Decision 6: every call is audited, and reads have their own table

Every tool call, reads included, is a row in `agent_audit`: the token, the tool, the
transport, the arguments after projection, the status and code, the correlation id and the
confirmation id. Only mutating calls, refusals and confirmation state changes also emit
`agent_action` into `activity_events`, with a `Result`, on the strip of the set they concern.

Reads stay out of the feed because the feed is bounded to 50,000 rows by owner decision (EPIC
Q, 2026-10-11) and one agent at the default rate limit could write 86,000 rows in a day. The
trail is pruned past `agents.audit_retention_days` (90) and read by `retnd agent audit`,
`GET /api/v1/agent/audit` and the Agents card.

The trail fails closed. The writer tracks whether its database writes are failing (a full or
read-only disk, a schema error), not only whether its queue is full. While that is set, a call
that needs an audit row is refused with 503 `AGENT_AUDIT_UNAVAILABLE` and nothing runs, and that
includes a raw bearer call that names no tool. The next successful write, a probe, clears it. I
chose losing availability during a journal fault over serving calls whose rows are gone.

## Decision 7: one projection at the tool boundary, and untrusted text is data

Every tool result passes `agenttools.Project` before it leaves: a field-name denylist
(`recipient`, `identity`, `bundle`, `password`, `secret`, `token`, `private_key`, `credential`,
`passphrase`, `key_material`) and the canary corpus's shape detectors (`AGE-SECRET-KEY-`,
`age1`, `age1pq1`, PEM private-key markers), over every string at every depth. The corpus
gains a row per transport and per token surface, each with its `_WouldCatchOne` control.

Each tool row names the result fields whose values originate outside the deployment: file
names, validator output, workflow logs, activity messages, remote hostnames. The transports
return those under an `untrusted` object in the structured result and never in the
human-readable text, a description or a prompt. The prompt-injection corpus plants an
instruction in each origin and asserts it arrives as data and that no confirmation exists
afterwards.

What the projection cannot do is hide the product's own nouns: set names, artifact names,
medium ids and sizes are what the agent is there to read. The site's honest section says so.

## Decision 8: the token never travels on a command line

The stdio transport cannot read its token from stdin, because stdin is the protocol. The verb
takes `--token-file PATH`, refused when the file is group- or world-readable, or
`RETND_AGENT_TOKEN` for hosts that can only pass an environment. There is no `--token` flag,
and a guard reads the flag set to say so. The rule is the one `auth enroll` already states
in its usage line (`core/cmd/retnd/auth.go:524`).

## Decision 9: the HTTP transport is off until an operator turns it on

`agents.http_transport` defaults to `off`, and `/api/v1/agent/mcp` answers
`AGENT_HTTP_TRANSPORT_OFF` until it is `on`. The engine is reached over whatever transport
`serve-ui` has, which on most LANs is plain HTTP; a long-lived bearer on a plain-HTTP SSE
stream is a worse exposure than a session cookie that expires. This is owner decision D1 with
my recommended default; the setting is a one-way door for an older build and the CHANGELOG
says so.

## Decision 10: one new module

The server speaks MCP through `github.com/modelcontextprotocol/go-sdk` v1.8.0 (the version in
`go.mod`), pinned, licensed Apache-2.0 and MIT (its LICENSE carries both texts), declared in
NOTICE and the licence inventory. A hand-rolled JSON-RPC subset was the alternative; it drifts
from a protocol with a published schema. If the module graph or the size gate refuses the SDK,
the fallback would have been a minimal implementation of `initialize`, `tools/*`, `resources/*`
and `prompts/*`. Neither refused it, so the SDK is what ships (D6).

## What this ADR does not decide

Per-set token restriction. Pre-approved mutations. Sampling, elicitation and OAuth. TLS. Any
change to the destructive gate. Which MCP clients are supported beyond the ones retnd-tests
drives.

## Owner decisions

D1 to D9 were answered by taking the recommended default, and nothing was changed from them. D10
was not in the scaffold: it came up while phase 1 was built and the owner answered it on #1357
(the `rat_` prefix), and decision 2 reads it into the token rule.

| ID | Question | Recommended | Answer |
|---|---|---|---|
| D1 | HTTP transport on by default? | Off | Off, as recommended. `agents.http_transport` defaults to `off` |
| D2 | Pre-approved `operate` tokens? | Not in this epic | Not in this epic, as recommended. Every mutation is confirmed |
| D3 | Token for the stdio verb | `--token-file` (0600), `RETND_AGENT_TOKEN` as the alternative | As recommended, and no `--token` flag |
| D4 | Bearer skips CSRF? | Yes, bearer only | Yes, bearer only, as recommended |
| D5 | Reads on the activity feed? | No, `agent_audit` only | No, as recommended |
| D6 | MCP implementation | The official Go SDK, minimal JSON-RPC as the fallback | As recommended; #1347 used the official Go SDK, `github.com/modelcontextprotocol/go-sdk` v1.8.0, with no fallback needed |
| D7 | Does `configure` ship? | Yes, minimal: enabled, retention, assurance | Yes, minimal, as recommended |
| D8 | Step-up on approve? | Yes | Yes, as recommended |
| D9 | Token expiry | 90 days default, one year ceiling | As recommended |
| D10 | When is a bearer an agent token? | Only with the `rat_` prefix | Only with the `rat_` prefix: any other bearer is not looked up as an agent token |

## Consequences

An agent is a caller this tree can name, with less authority than a person, whose reads are
recorded and whose writes pass through a person. The cost is a bearer credential in a tree
that had none, a confirmation queue an operator has to read, one migration, one module, and a
config block that is a one-way door. Two things are owed before 0.8.0: the audit rows an
unauthenticated invalid-token request writes need a bound (a per-IP limit or pruning, U1.4 and
U1.5), because anyone who can reach the port can mint them. The container contract does not move, and a release gate
proves it.
