# Letting an agent look: the MCP server

retnd can act as an MCP server, so a client that speaks the Model Context Protocol can read what
your deployment knows (sets, backups, copies, assurance, the activity feed) and, with your say-so,
ask for things to be started. This page is the user side of that: how to connect a client, what the
scopes mean, what a confirmation looks like from both ends, what the audit records, and what the
whole thing does not protect you from.

The design is in [ADR 0034](adr/0034-an-agent-is-a-scoped-client-of-the-api.md) and the contract in
[EPIC U](EPIC-U-mcp-server.md). I wrote this page so you do not have to read either to use it.

## What an agent is here

An agent is a client of `/api/v1` with a token of its own. It is not a second implementation of
anything: every tool it calls goes through the same route the web UI or the `retnd` verb would hit,
so it gets the same answer, the same error code and the same refusal. Nothing is configured until
you mint a token. A deployment that has never minted one has no agent access at all, and behaves
exactly as it did before this feature existed.

## Tokens

You mint a token from Settings (the Agents card) or from a terminal:

```
retnd agent token create triage                      # scope read, 90 days
retnd agent token create nightly --scope operate --expires-in 30d
retnd agent token list
retnd agent token revoke <token-id>
```

- The token is printed once, on a line of its own, and never again. After that the deployment keeps
  a SHA-256 of it and an eight-character prefix so you can tell tokens apart. `token list` and the
  matching route never show the hash.
- A token starts with `rat_`. A bearer that does not carry that prefix is never treated as an agent
  token.
- Every token has an owner (who minted it), an expiry (90 days by default, one year at most, there
  is no token that never expires) and a last-used time.
- Revoking is checked on every request, so a revoked token is refused on its very next call, even
  if an MCP session is still open.
- No flag takes a token. A secret on a command line lands in your shell history and a process
  listing, so a token only ever travels in a file, in an environment variable or in an
  `Authorization` header. (`agent audit --token ID` takes a token's id, never the token.)

Minting a token is session-only. No token, whatever its scope, can mint another.

Minting over plain HTTP wants the same acknowledgement key export does, because the token comes back
in the response. The engine refuses `POST /agent/tokens` over plain HTTP until it is acknowledged
(`INSECURE_TRANSPORT_ACK_REQUIRED`), so a remote `retnd agent token create` over plain HTTP exits 52
until `RETND_API_INSECURE_TRANSPORT=acknowledged` is set. The browser asks for the same consent in
the mint dialog, so the CLI and the browser behave alike. A loopback address needs neither.

## Scopes

There are three words, and they are closed (a config or a request with any other word is refused).
They nest: `operate` includes `read`, and `configure` includes `operate`.

| Scope | What a token can do |
|---|---|
| `read` | Look. Status, sets, backups, copies, encryption state (never keys), assurance, activity, quarantine, operations, storage, a retention preview, workflow runs. A `read` token starts nothing and changes nothing. It is the default, and the only scope anything suggests. |
| `operate` | Ask for an action to be started: run a cycle or a set, start a recovery test, verify a copy, start a scrub, retry a backup, revalidate a quarantined item. Every one of those waits for you (see confirmations). |
| `configure` | Ask for a small configuration change: enable or disable a set, set or clear a set's retention, set a set's assurance. Also waits for you. |

No scope reaches authentication, first-run, key export, import, decrypt, bundle confirmation,
recovery grants, SSH keys, storage credentials, workflow secrets, catalogue rebuild, quarantine
reinstate, retention apply, creating or deleting a set or a medium, or token minting. Those are rows
in the catalogue with a stated reason, not tools. `retnd agent tools` prints the whole table, the
scope of each tool and the reason for each refusal, and so does `GET /api/v1/agent/tools`.

## Connecting a client over stdio

This is the default path. The client starts `retnd agent serve --stdio`, which speaks MCP on its
standard input and output and calls the engine's API with your token.

1. Mint a token and put it in a file only you can read:

   ```
   retnd agent token create triage > ~/.config/retnd/triage.token
   chmod 600 ~/.config/retnd/triage.token
   ```

2. Tell the verb where the engine is. It is a client of the API, so it needs `RETND_API_URL`
   (the same variable `retnd status` uses beside a serving engine):

   ```
   RETND_API_URL=http://nas.example.net:8080 \
     retnd agent serve --stdio --token-file ~/.config/retnd/triage.token
   ```

3. Point your MCP client at that command. The exact config shape is the client's, but it comes down
   to a command (`retnd`), arguments (`agent serve --stdio --token-file <path>`) and an environment
   (`RETND_API_URL`).

`--token-file` is refused if the file is readable by its group or by anyone else. Standard input is
the protocol, so the token cannot come from there. For a client that can only pass an environment
and not a file, `RETND_AGENT_TOKEN` is the alternative. Neither shows up in a process listing.

## Connecting a client over HTTP (off by default)

The same server is also mounted at `/api/v1/agent/mcp` (streamable HTTP, with the bearer in the
`Authorization` header). It is off, and answers `AGENT_HTTP_TRANSPORT_OFF`, until you turn it on
with one line in `config.yaml`:

```yaml
agents:
  http_transport: on
```

I left it off on purpose. The engine is reached over whatever transport your `serve-ui` has, which
on most home networks is plain HTTP, and a long-lived bearer token on a plain-HTTP stream is a worse
exposure than a session cookie that expires. If you have a TLS terminator in front of retnd, turn it
on. If you do not, use stdio.

Turning it on is a one-way door for an older build: a `config.yaml` carrying an `agents:` block
cannot be parsed by a build from before this feature (see the CHANGELOG). The browser can never be
an MCP client by accident either: a session cookie presented to the MCP endpoint is refused.

The other four `agents:` settings, all optional, and what they default to:

| Field | Default | What it bounds |
|---|---|---|
| `agents.rate_limit_per_minute` | 60 | calls per token per minute; over it is `AGENT_RATE_LIMITED` with the wait |
| `agents.confirmation_ttl` | 15m | how long a pending confirmation lives before it expires |
| `agents.max_pending_confirmations` | 5 | open confirmations one token may have; the next is `AGENT_CONFIRMATION_LIMIT` |
| `agents.audit_retention_days` | 90 | how long `agent_audit` rows are kept before they are pruned |

## Result codes that are not API errors

A tool result can carry a code that never comes out of `/api/v1`: `INVALID_REQUEST`,
`ENGINE_UNREACHABLE` and `AGENT_RESULT_TOO_LARGE`, all with exit code 1. The last one is the one
result with no CLI twin, because the CLI would simply print the whole answer. A result is capped at
64 KiB. On a route that pages (it answers with a `next_cursor`) the server shrinks the page to fit.
On a route that does not, an answer over the cap is refused with `AGENT_RESULT_TOO_LARGE` rather than
cut, so an agent never mistakes a partial list for a complete one.

## Confirmations, from both sides

A tool call under `operate` or `configure` executes nothing.

**What the agent sees.** The call returns `AGENT_CONFIRMATION_REQUIRED` with a confirmation id and
the exact `retnd` command the request is equivalent to. That is an ordinary tool result, not a
protocol error, so the agent can tell its user what it asked for. Later it can call
`confirmation_status` with that id to read what you decided and, once it ran, how it ended. It only
ever sees its own.

**What you see.** A pending confirmation appears on the Agents card ("waiting for you") and in
`retnd agent confirmations list`. It shows the command, the set it concerns, the token that asked and
when it expires. You can read the request before anything happens:

```
retnd agent confirmations list
retnd agent confirmations show <id> [--json]
retnd agent confirmations approve <id>       # asks for your password (a step-up)
retnd agent confirmations deny <id>
```

Approving needs a step-up, so a dialog clicked through by reflex does not run anything. Approval
replays the stored request body byte for byte through the handler a person would have used, with the
token as the recorded actor and you as the approver. A body supplied at approval is refused
(`AGENT_CONFIRMATION_BODY_SUBSTITUTED`), so what you read is what runs. Denying ends it, and so does
the 15-minute expiry (`AGENT_CONFIRMATION_EXPIRED` if the agent comes back late).

Three more codes tell you what to do next: `AGENT_CONFIRMATION_NOT_FOUND` (404, there is no such
confirmation), `AGENT_CONFIRMATION_DECIDED` (409, somebody already approved or denied it) and
`AGENT_CONFIRMATION_TOKEN_ENDED` (409, the token that asked was revoked or expired before you
decided). Like the other refusals here they exit 1 from the CLI.

The replay meets every refusal the original would have met. The operation gate is shut by default
and this feature opens nothing: approving `run_cycle` on a deployment whose gate is shut gives
`DESTRUCTIVE_OPERATIONS_DISABLED` and exit status 50, exactly as `retnd run` does for you.

## The audit

Every call an agent makes, reads included, is one row in `agent_audit`: the token, the tool, the
transport, the arguments after projection, the status and code, the correlation id and the
confirmation id if there was one.

```
retnd agent audit [--token <id>] [--since 24h] [--json]
```

The same table is on the Agents card with a token and a time filter, and is
`GET /api/v1/agent/audit`. Only mutations, refusals and confirmation changes also reach the activity
feed (as `agent_action`, on the strip of the set they concern). Reads stay out of it because the
feed is bounded, and one agent at the default rate limit could otherwise push it out in a day. Rows
older than `agents.audit_retention_days` are pruned.

## The threat model, in one page

What the feature does for you:

- **A token is a credential with a shelf life**: hashed at rest, shown once, owned, expiring,
  revocable per request, with a closed scope vocabulary.
- **Nothing an agent reads can act on its own.** Descriptions, resource text and prompts are static
  strings, pinned by a test. Text that originates outside the deployment (file names, validator
  output, workflow logs, activity messages, remote host names) is returned inside an `untrusted`
  object as data, never in the human-readable text. A mutation is only ever a pending confirmation
  until a person with a step-up approves it.
- **The projection strips what must not leave.** Every result passes one filter before it goes out:
  a denylist of field names (`recipient`, `identity`, `bundle`, `password`, `secret`, `token`,
  `private_key`, `credential`, `passphrase`, `key_material`) and detectors for the shapes of key
  material (age secret keys, age recipients, PEM private-key markers), over every string at every
  depth. No tool exists over a route that carries key material or credentials.
- **It is a client of the API, not a back door.** The same refusals, the same codes, the same exit
  codes. The canonical container contract is unchanged.

What it cannot hide, and what a person still has to read:

- **The product's own nouns.** Set names, artifact names, medium ids, sizes and times are what the
  agent is there to read, and it reads them. If a file name is sensitive, an agent with `read` can
  see it.
- **A persuadable model.** I cannot prove a model will not be talked into proposing something by
  text it read. What is proven is that persuasion cannot act without you: the proposal is a
  confirmation with the real command on it, and you read the command before you approve it.
  Reading it is the part that cannot be automated away.
- **A transport that is not confidential.** Stdio keeps the token between two processes on one
  machine. The HTTP transport is only as private as the network and the TLS in front of it, which
  is why it is off until you say so.
- **Every client.** The same read and the same refusal were driven through the CLI, the browser and
  both transports and compared. That covers the clients the test suite drives, not every MCP client
  you might connect.

## Where else this is written down

- `retnd agent tools` and `GET /api/v1/agent/tools`: the live catalogue.
- [the reference](site/reference.html#cli-commands): every `retnd agent` verb.
- [`docs/api/contract.md`](api/contract.md): the routes and the bearer rule.
- [`docs/config-inventory.json`](config-inventory.json): the five `agents.*` fields.
- [ADR 0034](adr/0034-an-agent-is-a-scoped-client-of-the-api.md): the decisions and the owner answers.
