# Home Assistant integration

The `zaptec_exporter` custom integration reads the Go exporter's cached
Prometheus `/metrics` endpoint, the same snapshot that Alloy scrapes. All
entities share one local HTTP request every 30 seconds. Zaptec API polling
defaults to one minute for charger state/history and five minutes for Sense;
no additional Zaptec calls or Grafana credentials are needed for the integration.

This follows the myUplink Exporter custom-integration model. It is a separate,
read-only integration for Home Assistant Core, alongside the Supervisor app.
The app collects and exports data; the integration creates native entities.
It is not the unrelated community Zaptec integration and does not control the
charger.

Tested with Home Assistant Core **2026.10.4** and Python **3.14**. Installation
and operation on the target HAOS host still require verification.

## Install

1. Install and start [Zaptec Cloud telemetry](zaptec_cloud/DOCS.md). Enable
   `sense_enabled` if you want installation/phase measurements. Either value
   of `cloud_enabled` works: Alloy and Home Assistant can read the same cache
   independently.
2. Copy `custom_components/zaptec_exporter` into
   `/config/custom_components/zaptec_exporter` in **Home Assistant Core's
   configuration directory**. `manifest.json` must be directly inside that
   folder. Do not place this integration in `/addons`.
3. Restart Home Assistant Core.
4. Open **Settings → Devices & services → Add integration → Zaptec Exporter**.
5. Enter `http://local-zaptec-cloud:8080`. Verify the app's actual **Hostname**
   on its Info tab. `localhost` points to Core's container, not the exporter app.
6. Keep maximum API snapshot age at `900` seconds for the default one-minute
   charger/history and five-minute Sense intervals. For longer intervals, use a threshold longer than the
   longest configured poll interval plus its request timeout.

Create a ZIP for transfer from this checkout:

```sh
python3 scripts/package-integration.py dist/zaptec-exporter-integration-0.1.0.zip
```

Extract it in Core's configuration directory so it creates
`custom_components/zaptec_exporter/`. The ZIP includes only integration Python
and JSON files, not local configuration, credentials or bytecode caches.

No host port is needed on HAOS. If the exporter runs elsewhere, use a reachable
HTTP(S) exporter URL. Its endpoint has no built-in authentication; keep access
on a trusted network or behind a trusted HTTPS proxy.

## Entities

Entities are grouped into three devices:

| Device | Entities |
| --- | --- |
| Zaptec charger | Charge power, per-phase output current/voltage, allocated current, operation/phases modes, online state, session energy, completed-session totals and latest-session data. |
| Zaptec Sense | Installation power, phase current/voltage, phase charging/available current, neutral current, power limits, today's/yesterday's total and charging energy, source timestamps and hourly-coverage diagnostics. |
| Zaptec Exporter | Independent charger, archive and Sense API health and last-success timestamps. Disabled sources do not appear. |

Only fields actually present in the exporter become entities. Real zero values
are preserved; missing values are not converted to zero. New fields/devices
appear on later local polls without duplicating existing entities. Stable IDs
use the physical charger/installation UUID, metric and bounded phase/day labels.
Exporter-level diagnostic IDs are scoped to the config entry.

HTTP connection failures make all entities unavailable until recovery. An API
failure or expired successful-fetch timestamp makes only that source's
measurement entities unavailable; other healthy sources continue to work.
Source-health diagnostics remain available while the exporter can be reached,
so you can distinguish `off` (API failure) from `unavailable` (exporter unreachable).
The integration can be set up while API sources are down and discover readings
when they recover.

The integration honors the exporter's omission of stale phase values or
incomplete energy sums. Diagnostics show source timestamps and cached hourly
coverage; they do not claim a stale reading is current. Energy sensor
attributes include the reported calendar-day start where available.

Power/current/voltage measurements have units and measurement state classes.
Daily, active-session and corrected archived energy remain **energy gauges**,
without `total_increasing` or a guessed cumulative meter. They can be displayed
in dashboards and Recorder history, but are not automatically eligible as
cumulative sources in Home Assistant's Energy dashboard. This integration does
not import the separately downloaded historical JSON into Recorder/statistics.

## Verify on your installation

1. Compare Sense phase values, power and daily energy against `/metrics` or
   Grafana. Allow up to 30 seconds for Core's local refresh.
2. Stop the app. Entities should become unavailable after the next local
   request/timeout; start it again and confirm recovery with the same IDs.
3. Reload the integration or restart Core. Confirm there are no duplicate
   entities and each device still contains the expected sensors.
4. Inspect API-health diagnostics and app logs for collection failures. An idle
   charger can have old omitted observations despite a healthy REST fetch;
   API health and charger connectivity are different properties.

Automated tests use real Home Assistant coordinator/entity classes and cover
setup, URL normalization, response bounds, units, zero/missing values,
independent source freshness, failure/recovery and entity discovery.

```sh
python3.14 -m venv /tmp/zaptec-ha-tests
/tmp/zaptec-ha-tests/bin/pip install homeassistant==2026.10.4 prometheus-client==0.21.0 pytest pytest-asyncio
/tmp/zaptec-ha-tests/bin/python -m pytest tests/homeassistant -v
```
