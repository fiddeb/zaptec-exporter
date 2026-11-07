# Zaptec Go Prometheus exporter

Read-only Go exporter for Zaptec Go, with shared state storage and two source modes: REST polling and Azure Service Bus AMQP.

For Home Assistant OS, use the [Zaptec Cloud telemetry app](homeassistant/zaptec_cloud/DOCS.md).
It packages this exporter with optional Grafana Alloy, following the myUplink
app layout. API polling defaults to five minutes; Alloy scrapes the cache every
minute. It can also be scraped by an existing Alloy instance.

The [Zaptec Exporter custom integration](homeassistant/INTEGRATION.md) creates
native Home Assistant sensors from the same cached endpoint, without extra
Zaptec API requests or another copy of the account credentials.

The [Grafana charging dashboard](dashboards/README.md) visualizes charging,
phase readings, daily energy and collection health from the same Prometheus metrics.

## Configuration file (recommended)

All settings, including credentials, can be supplied in one JSON file. No environment variables are required. Create a private local copy of the example:

```sh
install -m 600 config.example.json config.json
```

Edit `username`, `password` and `charger_id` in `config.json`, then start the exporter:

```sh
go run .
curl http://localhost:8080/metrics
```

The exporter automatically reads `config.json` from the working directory. For another location, use `go run . -config /path/to/config.json` (or `./zaptec-exporter -config /path/to/config.json` after building). An explicitly requested missing file, malformed JSON or an unknown key fails startup. Relative paths such as `history_file` are relative to the process working directory.

Only these three keys are required in REST mode; other keys use the defaults shown in `config.example.json`:

```json
{
  "username": "your-service-account@example.com",
  "password": "your-password",
  "charger_id": "your-charger-uuid"
}
```

`config.json` is ignored by Git. Keep it readable only by the account running the exporter (`chmod 600 config.json`), and keep custom config paths out of version control as well. Passwords are literal JSON strings; shell-style environment expansion is not performed.

## Optional environment variables

Non-empty environment variables override file values. File values override built-in defaults. Existing environment-only deployments continue to work when the default `config.json` is absent.

| JSON key | Environment variable |
| --- | --- |
| `username` | `ZAPTEC_USERNAME` |
| `password` | `ZAPTEC_PASSWORD` |
| `charger_id` | `ZAPTEC_CHARGER_ID` |
| `mode` | `ZAPTEC_MODE` |
| `user_group_id` | `ZAPTEC_USERGROUP_ID` |
| `listen_addr` | `LISTEN_ADDR` |
| `poll_interval` | `ZAPTEC_POLL_INTERVAL` |
| `stale_after` | `ZAPTEC_STALE_AFTER` |
| `history_enabled` | `ZAPTEC_HISTORY_ENABLED` |
| `history_from` | `ZAPTEC_HISTORY_FROM` |
| `history_interval` | `ZAPTEC_HISTORY_INTERVAL` |
| `history_file` | `ZAPTEC_HISTORY_FILE` |
| `sense_enabled` | `ZAPTEC_SENSE_ENABLED` |
| `sense_interval` | `ZAPTEC_SENSE_INTERVAL` |
| `sense_timezone` | `ZAPTEC_SENSE_TIMEZONE` |

For example, REST mode can also be started entirely through environment variables:

```sh
export ZAPTEC_USERNAME='your-service-account@example.com'
export ZAPTEC_PASSWORD='...'
export ZAPTEC_CHARGER_ID='your-charger-uuid'
export ZAPTEC_MODE=rest
go run .
curl http://localhost:8080/metrics
```

Default REST polling: `5m`; `ZAPTEC_POLL_INTERVAL` changes it (minimum 30s). `ZAPTEC_STALE_AFTER` defaults to `10m`, leaving time for the next five-minute poll to finish. `LISTEN_ADDR` defaults to `:8080`. `go test ./...` works without external dependencies.

`charger_id` accepts either the charger UUID or its hardware serial number (for example `ZAG000001`). A serial number is resolved at startup using the authenticated charger's list; only an exact `deviceId` match is used. The resulting UUID is used for live state, archived sessions, AMQP filtering, cache identity and metric labels. If there is no unique match, startup stops with an explanatory error. UUID configuration skips the list lookup.

You can also copy the UUID from Zaptec Portal → Installation → Circuit → Charger details → Copy charger ID. API access needs the appropriate permissions. A dedicated service account is recommended. Do not commit secrets.

## Completed sessions and energy history

History synchronization is enabled by default in both source modes. It uses [`GET /api/sessions/archived`](https://docs.zaptec.com/reference/api_sessions_archived_get), follows its opaque page cursor, and filters on the configured charger. **This endpoint requires owner permissions on the charger**, which can differ from the permissions needed for live state. The deprecated `/api/chargehistory` endpoint is not used.

| Setting | Default | Meaning |
| --- | --- | --- |
| `ZAPTEC_HISTORY_ENABLED` | `true` | Set to `false` for live metrics only. |
| `ZAPTEC_HISTORY_FROM` | `1970-01-01T00:00:00Z` | Fixed inclusive lower bound on session end time, in RFC3339 format. Set a later start to limit backfill. |
| `ZAPTEC_HISTORY_INTERVAL` | `5m` | Delay between synchronizations; minimum `1m`. |
| `ZAPTEC_HISTORY_FILE` | `zaptec-history.json` | Persistent accounting cache; parent directory must exist and be writable. Mount it on persistent storage in containers. |

Each synchronization rereads the complete configured interval up to its start time. This catches delayed offline sessions and corrections to old sessions without relying on a moving lookback window. Pages are deduplicated by session ID; voided or replaced sessions are excluded from totals. A run is limited to five minutes and 1,000 pages of 50 sessions; if this is insufficient, narrow the configured range. Respect API fair-use limits when selecting the interval.

The cache is replaced atomically only after all pages have been fetched and saved successfully. Failed requests or disk writes keep the previous totals and set `zaptec_exporter_history_up` to zero. Before the first successful synchronization, accounting metrics are omitted. After restart, saved totals are available immediately; health stays down until a fresh synchronization succeeds. Corrupt files or a charger/start-time mismatch cause startup to fail; use a separate cache path when changing the charger or range.

The file stores session IDs, charger ID, timestamps, energy and correction flags, with file permissions `0600`. User identities, RFID tokens and signatures are not stored. The default cache filename is ignored by Git; keep custom cache paths out of version control too.

History metrics:

* `zaptec_charge_completed_energy_kwh`: sum of non-void completed session energy since `ZAPTEC_HISTORY_FROM`.
* `zaptec_charge_completed_sessions`: count of those sessions, including zero-energy sessions.
* `zaptec_charge_history_from_timestamp_seconds`: the configured coverage start.
* `zaptec_charge_last_session_energy_kwh`, `zaptec_charge_last_session_end_timestamp_seconds`, `zaptec_charge_last_session_duration_seconds`: the latest session with a known actual `endDateTime`. Duration includes connected time and charging pauses; invalid negative durations are omitted.
* `zaptec_exporter_history_up`: latest synchronization succeeded and is no older than twice the configured interval.
* `zaptec_exporter_history_last_success_timestamp_seconds`: start time of the last fully persisted synchronization.

All history metrics are **gauges**. Corrections can decrease totals, so do not apply `rate()` or `increase()` to them. Accounting metrics retain their last values during archive outages; monitor the separate history health metrics. Live observation expiry does not apply to completed sessions.

This persists session history but does not backfill old samples into Prometheus. Differences between scrapes can include late reports or corrections and are not exact daily/monthly consumption. For retrospective reports, the cache contains individual session energy and actual end times; session totals alone do not split consumption across midnight. Coverage is limited to sessions returned by Zaptec for the configured time range and permissions.

## Zaptec Sense (experimental, opt-in)

Sense uses the undocumented `/api/installation/{id}/energySensorData` endpoint demonstrated in [this community issue](https://github.com/custom-components/zaptec/issues/108). Its format has been checked against one installation; it is not an officially supported external API. Enable it explicitly in the config file:

```json
"sense_enabled": true,
"sense_interval": "5m",
"sense_timezone": "Europe/Stockholm"
```

Sense is disabled by default. When enabled, it fetches immediately, then waits five minutes between successful requests. The interval must be at least `5m`. Charger state and archived sessions also default to five-minute intervals. Prometheus scrapes only the in-memory cache and never trigger API requests.

The installation UUID is looked up from the configured charger once per process. Each Sense request covers yesterday's local midnight through the retrieval time, with `resolution=day1`. This supplies both the latest phase readings and hourly energy intervals in one request. Local calendar-day boundaries use `sense_timezone`, including 23/25-hour daylight-saving days; timezone data is bundled with the binary.

| Metric | Meaning |
| --- | --- |
| `zaptec_sense_current_amperes{phase="1|2|3"}` | Installation current per phase. |
| `zaptec_sense_voltage_volts{phase="1|2|3"}` | Voltage per phase. |
| `zaptec_sense_charge_current_amperes{phase="1|2|3"}` | Charging current reported by Sense. |
| `zaptec_sense_available_current_amperes{phase="1|2|3"}` | Available charging current per phase. |
| `zaptec_sense_neutral_current_amperes` | Neutral current. |
| `zaptec_sense_power_watts` | Latest total installation power reading, converted from API kW. |
| `zaptec_sense_power_limit_watts`, `zaptec_sense_dynamic_power_limit_watts` | Reported power limits, converted from API kW; omitted when unavailable. |
| `zaptec_sense_total_energy_kwh{period="today|yesterday"}` | Total energy reported by Sense for the selected calendar day. |
| `zaptec_sense_charge_energy_kwh{period="today|yesterday"}` | Charging energy reported by Sense for that day. |
| `zaptec_sense_energy_complete{kind="total|charge",period="today|yesterday"}` | All expected hourly slots are present. For today, this is coverage through the last retrieval, including the partial current hour. |

All Sense measurements are gauges and carry `installation_id`, since they describe the whole installation. Day labels are the fixed values `today` and `yesterday`; `zaptec_sense_day_start_timestamp_seconds` identifies each day's start. Today's totals reset at local midnight: do not apply `rate()` or `increase()` to them. Missing or duplicate hourly slots cause the affected energy sum to be omitted and its completeness metric to be zero. Missing phase fields are omitted; actual zero readings are retained.

`zaptec_exporter_sense_up` reports whether the latest fetch succeeded and is no older than twice `sense_interval`. Phase and power values also expire based on their source timestamps after twice that interval (10 minutes by default). Inspect `zaptec_sense_reading_timestamp_seconds` and `zaptec_sense_power_reading_timestamp_seconds` for source freshness. `zaptec_exporter_sense_last_success_timestamp_seconds` and `zaptec_sense_energy_as_of_timestamp_seconds` expose the retrieval time. Cached values keep their original timestamps during failures; energy sums are not fabricated or reset to zero.

Failures use exponential backoff up to one hour, and longer `Retry-After` delays are respected. A successful fetch resets the delay to five minutes. Sense failures do not stop the other sources. The API provides historical total power/energy, but only the latest per-phase reading; Prometheus builds phase history from the moment collection starts.

## Service Bus mode (experimental; not yet integration-tested)

When Zaptec has enabled a subscription on your User Group:

```sh
go get github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus
go run -tags servicebus .
```

Set `"mode": "servicebus"` and `"user_group_id": "your-user-group-uuid"` in the config file, or use `ZAPTEC_MODE=servicebus` and `ZAPTEC_USERGROUP_ID`. This uses a REST snapshot and periodic REST reconciliation while AMQP events update the same cache. The Azure SDK dependency cannot be downloaded in the provided build environment, so the AMQP build has not been compiled or exercised against a real subscription.

## Home Assistant OS and Alloy

The local app supports `aarch64` and `amd64`. Configure Zaptec credentials,
charger UUID/serial, optional Sense collection and Grafana Cloud remote-write
settings in Home Assistant's app Configuration tab. The entrypoint writes a
private exporter config file. Session history and Alloy state persist in `/data`.

After this checkout has been published to GitHub, install/update from a Home
Assistant terminal with `/addons` mounted, using the same flow as myUplink:

```sh
zaptec_update_dir=$(mktemp -d)
git clone --depth 1 --branch main \
  https://github.com/fiddeb/zaptec-exporter.git "$zaptec_update_dir" &&
sh "$zaptec_update_dir/scripts/package-homeassistant.sh" /addons/zaptec_cloud
```

Reload the app store, then install **Zaptec Cloud telemetry** under Local apps.
For unpushed local work, run `sh scripts/package-homeassistant.sh /tmp/zaptec_cloud`
from this checkout and transfer the folder to `/addons/zaptec_cloud` instead. The Supervisor
builds the packaged source; it does not fetch another version of the exporter.
No host networking, privileged mode, Docker socket or Home Assistant token is
required. Port 8080 is accessible internally and its optional host mapping is
disabled by default.

Set `cloud_enabled: false` if an existing Alloy will scrape the app. Use its
Info-tab Hostname (normally `local-zaptec-cloud:8080`), not `localhost`, as the
target. The [installation guide](homeassistant/zaptec_cloud/DOCS.md) includes the
Alloy block, options, update procedure and verification queries.

## Prometheus

```yaml
scrape_configs:
  - job_name: zaptec
    static_configs:
      - targets: ['localhost:8080']
```

## Metric notes

* `zaptec_charge_power_watts`: power (W), ID 513.
* `zaptec_charge_session_energy_kwh`: active session energy (kWh), ID 553; **gauge, not counter** because it resets each session.
* `zaptec_charge_current_amperes{phase="1|2|3"}`: output current, IDs 507-509.
* `zaptec_charge_voltage_volts{phase="1|2|3"}`: output voltage, IDs 501-503.
* `zaptec_charge_current_limit_amperes`: allocated current, ID 708.
* `zaptec_charger_operation_mode`: operation mode enum, ID 710.
* `zaptec_charger_online`: online observation, ID -2.
* `zaptec_charge_phases_mode`: phases enum, ID 519.
* `zaptec_exporter_up`: fresh REST snapshot; not a measure of AMQP connection health.
* Observations older than `ZAPTEC_STALE_AFTER` are **omitted**; no fabricated zeros.
* REST metric sample freshness is determined from the Zaptec observation timestamps, not just polling time.
* Observation timestamps without a timezone suffix are interpreted as UTC. Both these REST timestamps and RFC3339 timestamps with a timezone are supported.
* A successful snapshot sets `zaptec_exporter_up` to `1` even while the charger is idle. Individual observations can still be omitted because they are old.

### Known limitations

* REST state decoding and serial resolution have been checked against one Zaptec Go account.
* AMQP adapter is source-only and unverified pending dependency download/subscription access.
* Prometheus exposition uses standard text format (no external Prometheus SDK).
* Archive pagination and corrections are covered by HTTP-fixture tests; delayed offline sessions and corrections have not been live-tested.
* AMQP connection-health metrics are not implemented yet.
* Snapshot frequency should be adjusted with respect to Zaptec API fair-use requirements.

References: https://docs.zaptec.com/docs/api-authentication , https://docs.zaptec.com/reference/api_chargers_id_state_get , https://docs.zaptec.com/docs/state-observation-reference , https://docs.zaptec.com/docs/zaptec-service-bus-subscriptions , https://docs.zaptec.com/reference/api_sessions_archived_get
