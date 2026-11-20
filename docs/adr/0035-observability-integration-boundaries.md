# ADR 0035: Where the observability integrations stop

- Status: accepted for decisions 2 and 5, which landed with O1.1 (#899). Decisions 1, 3 and 4 are
  decided in the spec and land with O1.3 (#901) and O1.2 (#900); this record is the place they
  are written down, and #903 confirms them when the epic closes.
- Date: 2026-11-19
- Scope: EPIC O (#898), FR-99 through FR-104. Recorded under O1.1 (#899) against
  `docs/EPIC-O-observability-integrations.md`.
- Number: `docs/adr/` ended at `0034`, and `0023` was already taken, so this is `0035`. The gaps
  at `0024` to `0026`, `0031` and `0032` stay gaps.
- Builds on ADR 0034 (the agent token, whose custody shape the scrape credential copies and whose
  authority it does not get), and ADR 0027 and 0028 (a secret is a reference or a digest, never
  inline in `config.yaml`). Supersedes nothing.

## Context

This product already rendered 46 Prometheus metric families, wrote its log as newline-delimited
JSON and had an alert dispatcher, and none of it reached anything an operator runs: nothing served
the exposition, nothing pinned the log's field names, and the only alert sink refuses on every
shipped deployment. Wiring each one up is small. Wiring them up wrong is easy, and each wrong way
is one a later reader would reasonably try again, so the five boundaries the spec's panel settled
are written down here with the alternative that lost.

## Decision 1: one alert sink, selected and never fanned out

`observability.alerts.delivery` is `platform` or `webhook`, and exactly one `alert.Sink` is built
either way. There is no registry, no chain and no fallback, and a test counts constructions.

Rejected: a webhook sink beside the platform sink. §71 and `core/service/alerts.go` give a framework
nowhere to start, and a second sink is that framework's first line. Every case anyone has asked for
is covered by choosing.

## Decision 2: `/metrics` sits outside `/api/v1` with a credential of its own

The scrape endpoint is registered beside `/health/live` on the engine's root router, outside the
`/api/v1` group, so it meets no `recordActions`, no CSRF, no destructive gate and no agent audit.
It takes an `rsc_` token minted by the product and shown once, kept as a SHA-256 digest and an
eight-character display prefix in a 0600 file. Disabled it answers `404`. The root routes that
live outside `/api/v1` are one list, `cliecho.RootRoutesOutsideAPI`, and every guard reads it.

Rejected: putting it in the group, which would write about 5,760 action-log rows a day per scraper
into a cursor-paged feed. Rejected: reusing the `rat_` agent token. An agent token is a principal
over a closed catalogue of `/api/v1` reads, every use is an audit row that fails closed while the
journal cannot be written, and it expires within a year. A scrape every 15 seconds would fill the
audit, and a full disk would stop the scrape, which is the moment the scrape matters most. Each
credential is the smaller blast radius only while neither opens the other's route, and a test holds
both directions. Rejected: argon2id. The token is 256 random bits, so a slow hash adds nothing
against guessing and hands an unauthenticated caller a CPU amplifier.

## Decision 3: no schema version on each log line

The log schema's version is the `schema="1"` stream label the shipped pipelines set and an attribute
on the once-per-process `startup` event.

Rejected: a `schema_version` field on every line, which costs bytes on every line of a high-volume
stream to answer a question that changes once a year.

## Decision 4: private webhook targets are allowed

The webhook refuses a non-HTTP scheme, plain `http` without `allow_insecure_http`, a URL carrying
userinfo and every redirect, and it never lets TLS verification be turned off. It does not refuse a
private or link-local address.

Rejected: blocking private ranges. The common correct target is `http://alertmanager:9093` on a
compose network, and the bundle this epic ships points there. The real exposures are the redirect
and the URL that is itself a credential, and those are what the guards cover.

## Decision 5: `/metrics` is on the engine, not forwarded by `web-ui`

The engine answers `/metrics` on its existing listener (`retnd:8080` on the compose network). The
published `web-ui` port still forwards only `/health/*` and `/api/v1/*`, so the canonical compose
file and the eleven providers' files change by zero lines.

Rejected: a `/metrics` pass-through in `apps/common/webhost/serve/ui.go`. It needs no compose edit,
but it puts a bearer-protected exposition on the one port published to the LAN, which is a larger
decision than this epic's. It is a one-line change if a remote scraper is ever needed.

## Consequences

- A deployment that never writes `observability:` behaves as before: `/metrics` is `404`, the log
  stream is unchanged except for the `startup` attribute, and alerts go where they always did.
- `observability:` is one new top-level key, so a `config.yaml` that carries it cannot be read by an
  older build. Deleting the block is the rollback. A minted scrape credential's file stays behind,
  inert.
- The scrape endpoint is reachable from the compose network only, and a Prometheus outside it has
  to join it or go through a proxy the operator runs.
