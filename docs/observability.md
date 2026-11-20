# Observability

This page covers the log stream (FR-100, EPIC O). The scrape endpoint (#899) and the alert webhook (#901) have landed and are set from the same Settings card and `retnd settings observability`; they don't have sections here yet, and the dashboards and the example bundle are #902's.

## The log stream is a schema

The engine writes newline-delimited JSON to stdout, one object per event, and the one-shot CLI verbs write the same stream to stderr. There is no other output mode and no toggle to switch to one: a switch whose off position does not exist would only be a control I could not explain.

What I added is a promise about the field names. A Loki pipeline, a `jq` filter or a dashboard is written against field names, and renaming one is exactly as breaking as renaming an event, so the eight reserved keys are a wire contract in the same sense `core/internal/obs/events.go` already claims for the 54 event names.

| Key | On which lines | What it is |
|---|---|---|
| `time` | every line | RFC 3339 timestamp with nanoseconds |
| `level` | every line | `DEBUG`, `INFO`, `WARN` or `ERROR` |
| `msg` | every line | the human-readable summary |
| `event` | every line | one of the 54 event names, which are pinned by name |
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

`event`, `action`, `action_id` and `backup_set` are not labels, on purpose. A Loki label is a stream dimension, and every distinct value is a new stream, which is an index entry and a chunk cut early. `action_id` is minted per action, so promoting it means one stream per cycle, per connection test and per API call, forever. That is unbounded label cardinality, and Loki bills you for it in index size, which is not a thing a dashboard shows you until the install is already slow. `event` and `backup_set` are bounded but multiply (54 events, times your backup sets, times four levels) for no query a field filter does not already answer.

The shipped configs are the documentation that executes. `core/tests/logpipeline` runs the real Promtail and Alloy images over a log file written by the real logger, receives what they push, and reads the label set off the wire. Its control runs the same thing over a config that promotes `action_id` and watches the assertion refuse it. Run it with `scripts/observability/log-pipeline.sh` (it needs a Docker daemon and fails without one rather than skipping).

There's one catch, and it's on the Loki side. Loki 3.2.1 adds a fifth label, `service_name`, to every stream it stores unless its own config file sets `discover_service_name: []` under `limits_config`. The command-line flag doesn't stop it (that was measured still adding the label); only the config file does. So the four labels above hold only on a Loki whose config has `discover_service_name: []`. The example bundle's `container/observability/loki/loki.yaml` sets it. `core/tests/logpipeline` can't see this, because its receiver stands in for Loki and never adds a label of its own, so the requirement lives here and `core/internal/obs/lokilabels_doc_900_test.go` keeps this paragraph from being dropped.

## Shipping logs is an egress decision

Pointing Promtail or Alloy at the stream sends it to a store you chose, and I do not control what that store keeps or who reads it. The stream can carry operator-authored text: hook script output, `msg` strings, refusal reasons. If an operator prints a password in a hook, it is in the stream, and nothing in this product can promise otherwise.

What FR-104 does promise is narrower and mechanised: no *resolved* secret this product holds (a repository passphrase, an S3 secret key, an SMTP password, a scrape token, a webhook credential) appears in any log line, and the corpus in `core/tests/observabilitycorpus` (it lands with #903) proves it for the log stream, the endpoint and the webhook. That is the honest boundary: it covers secrets the product holds, and it cannot cover text a person typed into a hook.

Redaction of endpoint strings (`WithRedaction`, built from `config.Remote.Sensitive`) is opt-in per endpoint, so a deployment that opted nothing in still ships its `msg` strings unredacted. Treat the log store with the same care as the host.
