This is an example, not a supported deployment: nothing installs it, no provider ships it, and nobody upgrades it for you.

# The retnd example observability bundle

Prometheus, Alertmanager, Loki, Promtail (or Alloy) and Grafana, wired to one retnd deployment, with two dashboards and a set of alerting rules (EPIC O, FR-102, #902). It is here to show what the engine's scrape endpoint and log stream look like plugged into the usual tools, and to be copied from. It is not a product surface: owning Grafana's upgrades, its login page and its store-submission consequences across eleven providers is a different job, and this directory does not take it on.

The canonical deployment does not change for any of this. `container/compose.yaml`, every provider's compose file, the Unraid and TrueNAS templates and the packaging manifests are exactly what they were, `/metrics` is served by the engine on the listener it already had, and no port is added anywhere.

## What is in here

| Path | What it is |
| --- | --- |
| `compose.observability.yaml` | Its own compose project (`retnd-observability`), run beside the canonical one |
| `prometheus/prometheus.yml` | One scrape job, `retnd`, reading the bearer token from a file you mint |
| `prometheus/rules/retnd.rules.yml` | Alerting rules over the `retnd_*` series |
| `alertmanager/alertmanager.yml` | One receiver, deliberately wired to nothing (add yours) |
| `grafana/provisioning/` | The Prometheus, Loki and Alertmanager datasources and the dashboard provider |
| `grafana/dashboards/` | Two dashboards: **Backup health** and **Workflow hooks** |
| `loki/loki.yaml` | Single-node Loki with usage reporting off and `discover_service_name` emptied, so it keeps the four documented labels |
| `promtail/promtail.yaml`, `alloy/config.alloy` | The log pipeline, four labels and nothing else (authored with the log schema, #900) |

## Before you start

1. **Turn the scrape endpoint on and mint a token.** In Settings, the Observability card, or from a terminal: `retnd settings observability patch --metrics-enabled` to enable metrics, then `retnd settings observability rotate-scrape-token`. The token is shown once, on the line that starts `rsc_` (the command also prints a status line and its startup log lines, so keep only that line: `retnd settings observability rotate-scrape-token | grep '^rsc_' > scrape-token`). Put it in a file on the host readable by uid 65534 (the user Prometheus runs as), for example `chmod 0400` and `chown 65534`. Minting again replaces the token at once, so update the file and restart Prometheus when you rotate.
2. **The canonical stack has to be running**, because Prometheus joins its `retnd_internal` network, which only exists while that project is up.
3. **Pick a Grafana admin password.** There is no default, on purpose.

## Run it

```
cd container/observability
export GF_SECURITY_ADMIN_PASSWORD='...'
export RETND_SCRAPE_TOKEN_FILE=/path/to/scrape-token
docker compose -f compose.observability.yaml up -d
```

Without either variable, `docker compose` refuses to start and names the one that is missing. If your provider runs the stack under another project name or network (Unraid's templates put both containers on a user-defined network called `retnd`; a TrueNAS app gets its own project name), set `RETND_NETWORK` to the network the engine is on; `docker network ls` shows it. Grafana listens on `127.0.0.1:3000` (`GRAFANA_PORT` moves it); it is loopback only until you decide to publish its login page.

To use Alloy instead of Promtail:

```
docker compose -f compose.observability.yaml --profile alloy up -d --scale promtail=0
```

## Getting the logs to Loki

The shipped pipelines read plain newline-delimited JSON from `/var/log/retnd/*.log`, which is `RETND_LOG_DIR` on the host (`./logs` by default). The engine writes those lines to its stdout and Docker keeps them in its own wrapped format, so something has to put the plain lines in that directory. The simplest is a follower on the host:

```
docker logs -f --since 0s retnd-retnd-1 >> "$RETND_LOG_DIR/retnd.log" 2>/dev/null
```

If you would rather read Docker's json-file logs directly, add `- docker: {}` as the first Promtail stage (or `stage.docker {}` in Alloy) and mount the container log directory instead. The bundle does not do that for you, because it means giving a container every other container's logs too.

Until something writes there, Loki is empty. The **retnd log lines in the last hour** panel says so (0, or no data), so an empty log panel next to it is not read as a quiet night.

Log shipping is an egress decision. The stream can carry text somebody else wrote: hook script output, refusal reasons, `msg` strings. `docs/observability.md` says what the product does and does not promise about it.

## The dashboards

**Backup health** is FR-24's health report: whether the scrape answers, each set's state, freshness against its stale threshold, failed artifacts and failing relocations, quarantine (with the irrecoverable kind on its own line), and free space on each destination. **Workflow hooks** is EPIC L's: whether hooks can run at all (runner, remote connections, interrupted runs), finished runs by status, step failures, timeouts, remote hooks that could not run, output truncations, and run and step durations.

Every panel's title says what it looks like when nothing is wrong, because an empty graph and a healthy one look the same unless the title tells them apart. A deployment that runs no workflows has no workflow series at all, so the whole Workflow hooks page is empty there, and that is the truth rather than a fault.

There are no panels for things retnd does not emit. `scripts/observability/check-dashboard-metrics.sh` (run by `scripts/ci-local.sh`) renders the engine's real exposition from a fully loaded health report and workflow counter set and fails on any `retnd_*` name, label or closed-vocabulary value in a dashboard or rules file that the renderers do not produce, on any Loki label or field the shipped pipeline and log schema do not carry, and on any panel whose title does not say what healthy looks like. Edit the JSON here rather than in Grafana's UI (the provisioning is read-only for that reason), and run the script.

## Alerting

Prometheus evaluates `prometheus/rules/retnd.rules.yml` and sends what fires to Alertmanager. That is the recommended topology. The engine can also push alerts itself (`observability.alerts.delivery: webhook`, with the `alertmanager` dialect), which is for deployments that scrape nothing; running both gives you two sources of truth about the same stale backup. If you do want the push, the engine has to reach Alertmanager, which means Alertmanager joining `retnd_internal` too, with the same peer caveat as below.

The one receiver in `alertmanager/alertmanager.yml` delivers nowhere, because every real notifier needs an address or a credential this repository will not carry. Alerts still show in Alertmanager and in Grafana's alerting view. Add your notifier there, reading its secret from a file.

## What joining the network means

The engine publishes no port, and `web-ui` forwards `/health/*` and `/api/v1/*` and nothing else, so `/metrics` is reachable on the canonical stack's `internal` network and nowhere else. That is why Prometheus joins `retnd_internal`. It also makes Prometheus a direct peer of the engine, a position `container/compose.yaml` otherwise gives to `web-ui` alone (the engine trusts forwarded headers from its peers on that network). So Prometheus is the only service here that joins it, and Grafana, Loki and Alertmanager stay on the bundle's own network.

A Prometheus that is not on this host reaches the endpoint through a reverse proxy you run, not through `web-ui`.

## Cleaning up

```
docker compose -f compose.observability.yaml down -v
```

removes this project's containers, its network and its four volumes, and leaves the canonical stack alone.
