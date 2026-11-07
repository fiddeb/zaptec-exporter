# Zaptec Cloud telemetry for Home Assistant OS

This local app follows the same layout as the myUplink Cloud telemetry app:
a Go exporter with optional Grafana Alloy in a Supervisor-managed container.
It supports `aarch64` (including Raspberry Pi 5 with 64-bit HAOS) and `amd64`.

The exporter polls charger state, archived sessions and optional Sense data
every **300 seconds**. Alloy scrapes the cached `/metrics` endpoint every
**60 seconds** by default and forwards samples with Prometheus remote write.
Scraping does not cause extra Zaptec calls. The app Log tab contains exporter
and Alloy logs.

## Install

Like the myUplink fork, this repository supplies a local build context rather
than prebuilt images or a one-click Supervisor repository. In Advanced SSH &
Web Terminal, with `/addons` mounted, run:

```sh
zaptec_update_dir=$(mktemp -d)
git clone --depth 1 --branch main \
  https://github.com/fiddeb/zaptec-exporter.git "$zaptec_update_dir" &&
sh "$zaptec_update_dir/scripts/package-homeassistant.sh" /addons/zaptec_cloud
```

Use this command for both installation and updates, just like myUplink. It
packages the selected checkout into `/addons/zaptec_cloud`; saved options and
history under `/data` are retained. These commands require the implementation
to have been pushed to the GitHub repository's `main` branch. To install unpushed local
work, package this checkout and transfer the resulting folder to
`/addons/zaptec_cloud` using SSH or Samba.

1. Open **Settings → Apps → Install app**, reload/check for updates, and find
   **Zaptec Cloud telemetry** under **Local apps**. Older Home Assistant versions
   call these Add-ons and the Add-on store.
2. Select **Install**. Supervisor builds the image for your architecture.
3. Set the options below in **Configuration**, then Save and Start.
4. Enable Start on boot. Keep Protection mode enabled.

The app needs internet access, but no host networking, Docker socket,
Home Assistant API token or privileged access.

## Options

| Option | Value |
| --- | --- |
| `username`, `password` | Your Zaptec account credentials. |
| `charger_id` | Charger UUID or hardware serial, such as `ZAG000001`. |
| `poll_interval` | Charger polling delay in seconds; default `300`. |
| `history_enabled` | Fetch completed sessions; default `true`. Charger owner permission is required. |
| `history_from` | Inclusive session end-time coverage start; default `1970-01-01T00:00:00Z`. |
| `history_interval` | Archive polling delay in seconds; default `300`. |
| `sense_enabled` | Enable Sense explicitly; default `false`. Requires a Sense-equipped installation and uses the experimental undocumented endpoint. |
| `sense_interval` | Sense polling delay in seconds; default `300`. Failures back off. |
| `sense_timezone` | Calendar-day timezone; default `Europe/Stockholm`. |
| `cloud_enabled` | Start the bundled Alloy; default `true`. Set `false` to use an existing external Alloy. |
| `metrics_url` | Grafana Cloud Metrics **remote-write URL**, not the Grafana dashboard URL. |
| `metrics_user` | Grafana Cloud Metrics instance ID. |
| `metrics_token` | Token with `metrics:write` permission. |
| `instance` | Stable metric label; default `homeassistant`. |
| `scrape_interval` | Alloy cache scrape interval in seconds; default `60`. |

All API intervals have a minimum of 300 seconds in the app. The ordinary
observation freshness limit is twice the charger poll interval. Sense phase
and power freshness is twice the Sense interval. API polling and Alloy scraping
are independent.

Supervisor stores options in `/data/options.json`. The entrypoint creates a
private `0600` `/data/exporter-config.json`; the exporter reads all its options
from that file. Grafana credentials are supplied only to Alloy. Keep app backups
private because they contain credentials.

The completed-session cache is `/data/zaptec-history.json`, and Alloy's remote
write state/WAL is `/data/alloy`. Both survive restarts and normal updates.
Alloy's WAL is a delivery buffer, not a queryable local metrics database.

## Use an existing Alloy

Set `cloud_enabled: false`; Cloud options may then be empty. Other Home Assistant
apps can scrape port 8080 over the internal app network without publishing a
host port. Verify the actual **Hostname** on this app's Info tab; for a local
installation it is normally `local-zaptec-cloud`.

Add this block to your existing Alloy configuration, changing the receiver
name to the remote-write component you already use:

```alloy
prometheus.scrape "zaptec" {
  targets = [{
    __address__ = "local-zaptec-cloud:8080",
    job         = "zaptec",
    instance    = "homeassistant",
  }]
  metrics_path    = "/metrics"
  scrape_interval = "60s"
  scrape_timeout  = "5s"
  forward_to      = [prometheus.remote_write.cloud.receiver]
}
```

`localhost` works for the bundled Alloy because it shares this app's container.
It refers to a different container when Alloy runs as a separate app.
If Alloy runs on another machine, enable the app's optional 8080 host port and
target `HOME_ASSISTANT_IP:HOST_PORT` instead. Metrics are unauthenticated;
publish that port only on a trusted network. The host port is disabled by default.
Use one scraper path for a given remote-write destination to avoid sending the
same series twice.

## Verify

In the app Log tab, check for a successful snapshot and the listening message.
An initial scrape may fail while the first API fetch completes. In Grafana,
query:

```promql
up{job="zaptec"}
zaptec_exporter_up{job="zaptec"}
zaptec_exporter_history_up{job="zaptec"}
zaptec_exporter_sense_up{job="zaptec"}
zaptec_sense_current_amperes{job="zaptec"}
zaptec_sense_total_energy_kwh{job="zaptec",period="yesterday"}
```

`up` means Alloy can scrape the app. The exporter health metrics describe the
different API fetches; disabled sources have no health metric. Old phase values
are omitted, and energy sums are omitted if hourly slots are missing. These
conditions do not imply that Prometheus scraping itself has failed. Day totals
are gauges; do not use `rate()` or `increase()` on them.

To create native Home Assistant sensors from the same cached values, install
the [Zaptec Exporter custom integration](../INTEGRATION.md). It works with either
value of `cloud_enabled` and makes no additional Zaptec API calls.

## Package and update

From the checkout you want to install:

```sh
sh scripts/package-homeassistant.sh /tmp/zaptec_cloud
```

The script copies the app files and creates `exporter-source.tar` containing
only `go.mod` and root Go sources/tests. It excludes local credentials, history,
logs and Git metadata. Copying only `homeassistant/zaptec_cloud` is insufficient;
the Docker build also needs the generated archive. The image builds exactly those
sources and runs their Go tests. It uses the same Go `1.27.3` and Alloy `1.21.3`
image versions as the referenced myUplink app.

To upgrade, repackage a reviewed checkout into `/addons/zaptec_cloud`, reload
the app store, update/rebuild and restart the app. Do not uninstall to update;
uninstalling may remove `/data`. Repackaging files alone does not replace the
running image. Increment `config.yaml`'s version for new app releases.

Host-side checks:

```sh
go test -race ./...
python3 -m unittest discover -s homeassistant/zaptec_cloud -p 'test_*.py'
sh scripts/package-homeassistant.sh /tmp/zaptec_cloud
docker build -t zaptec-cloud:local /tmp/zaptec_cloud
```

Supervisor installation still needs verification on the target HAOS host.
