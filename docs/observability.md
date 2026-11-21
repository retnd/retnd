# Observability

This is the one page to read for EPIC O: the scrape endpoint, the log stream, alert delivery to a webhook, and the example bundle. All of it is off (or exactly what it was) until you change something: `/metrics` answers `404`, the log stream is the one the engine always wrote, and alerts go where they always went. Everything here is set from the Observability card in Settings, from `retnd settings observability`, or from `PATCH /api/v1/settings/observability`; `docs/deployment.md` has the long form of the endpoint and the webhook, and ADR 0035 has the decisions behind them.

## The scrape endpoint

`GET /metrics` is served by the engine, on the listener it already has (`retnd:8080` on the compose network's `internal`), beside `/health/live` and outside `/api/v1`. The published `web-ui` port does not carry it: `web-ui` answers `404` for `/metrics` itself. A Prometheus reaches it by joining `retnd_internal` (the example bundle does exactly that) or through a reverse proxy you run.

```sh
retnd settings observability patch --metrics-enabled
retnd settings observability rotate-scrape-token > retnd-scrape-token
```

The second command prints a `rsc_` token once, and its standard output is the token and nothing else. Running it again replaces the token at once, with no overlap window, so update the scraper's `credentials_file` and reload it straight after. Only a SHA-256 digest and an eight-character display prefix are kept, in a 0600 file in a 0700 directory beside `config.yaml`. A token written into `config.yaml` is refused at load. The Settings card mints the same way and shows the token once, so an operator with only a browser can wire Prometheus.

Prometheus sends the token as a bearer (`authorization: credentials_file:`) or as the `basic_auth` password; the username is ignored. Off, the answer is `404`. On, no credential, a wrong one or a malformed header is `401` with an empty body; ten refusals from one peer address in a minute and that address gets `429` with `Retry-After` for the rest of the minute, valid token or not. The address is the connection's own peer; `X-Forwarded-For` is ignored on this route. Every answer carries `Cache-Control: no-store`. Refusals are counted in `retnd_scrape_rejected_total{reason}` (`disabled`, `unauthenticated`, `method`), and a scrape writes no action-log row. The health half is rebuilt at most once per `observability.metrics.min_rebuild_interval` (default `15s`, at least `1s`, at most `2m`), and `retnd_report_generated_timestamp_seconds` says how old it is. The seven workflow families are absent, not zero, in a process that never built a workflow engine.

The scrape token and an agent's `rat_` token never open each other's routes.

## The log stream is a schema

The engine writes newline-delimited JSON to stdout, one object per event, and the one-shot CLI verbs write the same stream to stderr. There is no other output mode and no toggle to switch to one: a switch whose off position does not exist would only be a control I could not explain.

What I added is a promise about the field names. A Loki pipeline, a `jq` filter or a dashboard is written against field names, and renaming one is exactly as breaking as renaming an event, so the eight reserved keys are a wire contract in the same sense `core/internal/obs/events.go` already claims for the 56 event names.

| Key | On which lines | What it is |
|---|---|---|
| `time` | every line | RFC 3339 timestamp with nanoseconds |
| `level` | every line | `DEBUG`, `INFO`, `WARN` or `ERROR` |
| `msg` | every line | the human-readable summary |
| `event` | every line | one of the 56 event names, which are pinned by name |
| `result` | a line that states an outcome | `success`, `warn`, `error` or `info` |
| `action` | a line that opens or closes a bracketed action | `cycle`, `workflow_run`, `workflow_step`, `recovery`, `recovery_test` |
| `action_id` | the same lines | minted per action, so a start and its completion pair by something other than spelling |
| `backup_set` | any line with a backup set in scope | the backup set id |

Everything else on a line is a per-event attribute (`version` on `startup`, `duration` on an action's completion, and so on). Those can be added freely and are never reserved. `core/internal/obs/schema_test.go` holds the list against literals typed out again in the test, runs a golden line over the real output, and plants the three drift shapes (a ninth key, `action_id` renamed to `actionId`, `result` dropped from a marked line) to show each is refused and that the refusal names the key. A per-event attribute stays green on purpose.

The version of the schema is `1`. It lives in two places and neither is a per-line field: the `log_schema` attribute on the `startup` event (every process emits it once, and it's written whatever the log level is, so a deployment set to `warn` still says which schema it writes), and the `schema` stream label the shipped pipelines set. A per-line version would cost bytes on every line of a high-volume stream to answer a question that changes once a year, and Loki already has a place for it.

## The log level

`observability.logs.level` (`debug`, `info`, `warn` or `error`, default `info`) is the configured floor. The environment still wins, because a knob someone reaches for in the middle of an incident should not need a config edit:

1. `RETND_DEBUG=1` (its deprecated alias `RM_DEBUG=1` too) gives `debug`.
2. A parseable `LOG_LEVEL` (`debug`, `info`, `warn`, `error`) is next.
3. Otherwise the configured floor. An unset or unparseable `LOG_LEVEL` defers to it. With nothing configured the floor is `info`, so a deployment that never writes the block logs exactly what it always did.

`GET /api/v1/settings/observability` and `retnd settings observability` report the level in force and where it came from, as one of two values: `config` when the configured floor decided, or `environment` when either variable above did. So "why is it not debug" is never a guess.

The floor lives in a `slog.LevelVar` inside the logger, shared by every logger derived from it, so a `PATCH` takes effect on the next line without a restart. It moves the engine only. The `web-ui` container has no `config.yaml` and keeps reading the environment alone, which is why the control's tooltip says which process it moves. It is not per backup set, because one process-wide writer cannot honour that: the daemon, API and startup lines have no set to attribute.

## Getting the lines to a shipper

The engine writes its lines to stdout. Both shipped pipelines read plain NDJSON from `/var/log/retnd/*.log`, and getting the lines there is the operator's job: nothing in the engine writes that directory. The example bundle's `container/observability/README.md` (#902) shows a `docker logs -f` follower that does it, and how to read Docker's own json-file output instead by adding a `docker` stage first.

## Labels: four, and nothing else

The shipped Promtail and Alloy pipelines (`container/observability/promtail/promtail.yaml` and `container/observability/alloy/config.alloy`) label a stream with exactly `job`, `instance`, `level` and `schema`, and parse everything else as a field. A query reaches the rest with `| json`:

```logql
{job="retnd", level="error"} | json | event="cycle_end" | backup_set="alpha"
```

`event`, `action`, `action_id` and `backup_set` are not labels, on purpose. A Loki label is a stream dimension, and every distinct value is a new stream, which is an index entry and a chunk cut early. `action_id` is minted per action, so promoting it means one stream per cycle, per connection test and per API call, forever. That is unbounded label cardinality, and Loki bills you for it in index size, which is not a thing a dashboard shows you until the install is already slow. `event` and `backup_set` are bounded but multiply (56 events, times your backup sets, times four levels) for no query a field filter does not already answer.

The shipped configs are the documentation that executes. `core/tests/logpipeline` runs the real Promtail and Alloy images over a log file written by the real logger, receives what they push, and reads the label set off the wire. Its control runs the same thing over a config that promotes `action_id` and watches the assertion refuse it. Run it with `scripts/observability/log-pipeline.sh` (it needs a Docker daemon and fails without one rather than skipping).

There's one catch, and it's on the Loki side. Loki 3.2.1 adds a fifth label, `service_name`, to every stream it stores unless its own config file sets `discover_service_name: []` under `limits_config`. The command-line flag doesn't stop it (that was measured still adding the label); only the config file does. So the four labels above hold only on a Loki whose config has `discover_service_name: []`. The example bundle's `container/observability/loki/loki.yaml` sets it. `core/tests/logpipeline` can't see this, because its receiver stands in for Loki and never adds a label of its own, so the requirement lives here and `core/internal/obs/lokilabels_doc_900_test.go` keeps this paragraph from being dropped.

## Alert delivery to a webhook

`observability.alerts.delivery` is `platform` (the default, today's behaviour) or `webhook`, and exactly one channel is built either way. The webhook URL is kept by reference (`url_ref`: a `file`, an `env` variable or a `command`), never as a value, because a Slack, Teams or Discord URL carries its secret in the path. The reference is read again for every delivery. It is set in `config.yaml` only: the card, the CLI and the API change the format, the transport flag and the two intervals, and report whether a URL is set and whether it reads (`url_status`), never the URL.

Two fixed formats, neither templatable. `generic` posts one JSON object per occurrence and stays quiet while the condition lasts. `alertmanager` posts the v2 array, re-sends a still-true condition every `refresh_interval` (default `4m`) so Alertmanager does not expire it, and closes it with `endsAt` set to now when it clears; `resolve_ttl` (default `10m`) has to clear `refresh_interval` by at least 30s, one delivery attempt. If you run Prometheus, its rules into Alertmanager are the better source of truth for staleness, and the bundle ships them.

Refused: anything but `https` unless `allow_insecure_http` is set, a URL with `user:password@`, any redirect (a 3xx is a failed delivery and its `Location` is never followed or logged). Certificate checks cannot be turned off. Private targets such as `http://alertmanager:9093` are allowed on purpose. `docs/deployment.md` ("Delivering alerts to a webhook") has what an unreadable reference looks like and what a switch of channel keeps.

## Shipping logs is an egress decision

Pointing Promtail or Alloy at the stream sends it to a store you chose, and I do not control what that store keeps or who reads it. The stream can carry operator-authored text: hook script output, `msg` strings, refusal reasons. If an operator prints a password in a hook, it is in the stream, and nothing in this product can promise otherwise.

What FR-104 does promise is narrower and mechanised: no *resolved* secret this product holds (a repository passphrase, an S3 secret key, an SMTP password, a scrape token, a webhook credential) appears in any log line, and the corpus in `core/tests/observabilitycorpus` checks it for the log stream, the endpoint and the webhook, each as its own row. One limit is written into the corpus itself: no engine process holds an SMTP password (that belongs to the web host's account mailer), so for that one the corpus can only show it never reaches the three surfaces from where an engine could see it at all. That is the honest boundary: it covers secrets the product holds, and it cannot cover text a person typed into a hook.

Redaction of endpoint strings (`WithRedaction`, built from `config.Remote.Sensitive`) is opt-in per endpoint, so a deployment that opted nothing in still ships its `msg` strings unredacted. Treat the log store with the same care as the host.

## Dashboards and the example bundle

`container/observability/` is a Prometheus, Alertmanager, Loki, Promtail (or Alloy) and Grafana stack wired to one deployment, with two dashboards and a set of alerting rules. It is an example and not a supported deployment, which is the first line of its README: nothing installs it, no provider ships it, and the canonical `container/compose.yaml` does not change by a line for it.

It is its own compose project. Prometheus joins the canonical stack's `retnd_internal` network, because that is the only place the engine's `/metrics` is reachable, and it is the only bundle container that does, because every container on that network is a direct peer of the engine. Grafana's admin password and the scrape token are required at `up` time with no default, and compose refuses to start without either, naming the variable. Grafana binds to loopback. Loki, Alloy and Grafana have their usage reporting turned off, so the example adds no egress you did not ask for. The bundle's Loki runs with `discover_service_name: []` in `loki/loki.yaml`, for the reason the labels section above gives, and `distribution/packaging/observability_bundle_902_test.go` fails if the list or its mount goes.

The two dashboards are **Backup health** (whether the scrape answers, state, freshness, failures, quarantine, capacity) and **Workflow hooks** (whether hooks can run, run and step outcomes, durations, truncations, remote-exec failures). Every panel's title says what it looks like when nothing is wrong, because an empty graph and a healthy one are otherwise the same picture. The rules in `prometheus/rules/retnd.rules.yml` feed Alertmanager, which is the recommended alerting topology; its one receiver is deliberately wired to nothing, because any real notifier needs an address or a credential.

A dashboard is a claim, so it is checked. `scripts/observability/check-dashboard-metrics.sh`, which `scripts/ci-local.sh` runs, renders the real exposition from a fully loaded health report and workflow counter set and fails on any `retnd_*` series, label or closed-vocabulary value a dashboard or rule names that the renderers do not emit, any Loki label the shipped Promtail pipeline does not set, any log field outside the eight reserved keys, any event name that is not in the catalogue, any datasource uid the provisioning does not define, and any panel title that does not say what healthy looks like. It plants a panel querying `retnd_backup_set_stale` (a name this product never emitted) every time it runs and requires that to be refused, naming the file, the panel and the name, and requires a panel over `retnd_backup_set_newest_good_backup_age_seconds` to pass. Edit the JSON in the repository, not in Grafana's UI (the provisioning is read-only for that reason), and run the script.
