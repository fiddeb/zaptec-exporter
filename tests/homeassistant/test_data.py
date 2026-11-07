"""Known exporter metrics, independent source health and bounded labels."""
from datetime import datetime, timezone

import pytest

from custom_components.zaptec_exporter.data import normalize_url, parse_snapshot

CHARGER = "11111111-1111-1111-1111-111111111111"
INSTALLATION = "22222222-2222-2222-2222-222222222222"
HEADER = """zaptec_exporter_up 1
zaptec_exporter_last_success_timestamp_seconds 1000
zaptec_exporter_history_up 1
zaptec_exporter_history_last_success_timestamp_seconds 1000
zaptec_exporter_sense_up 1
zaptec_exporter_sense_last_success_timestamp_seconds 1000
"""
POINTS = f"""zaptec_charge_power_watts{{charger_id="{CHARGER}"}} 0
zaptec_charger_online{{charger_id="{CHARGER}"}} 1
zaptec_charge_completed_energy_kwh{{charger_id="{CHARGER}"}} 120
zaptec_sense_current_amperes{{installation_id="{INSTALLATION}",phase="1"}} 2.5
zaptec_sense_charge_current_amperes{{installation_id="{INSTALLATION}",phase="1"}} 0
zaptec_sense_total_energy_kwh{{installation_id="{INSTALLATION}",period="today"}} 35.5
zaptec_sense_total_energy_kwh{{installation_id="{INSTALLATION}",period="yesterday"}} 58
zaptec_sense_charge_energy_kwh{{installation_id="{INSTALLATION}",period="yesterday"}} 17
zaptec_sense_reading_timestamp_seconds{{installation_id="{INSTALLATION}"}} 950
zaptec_sense_day_start_timestamp_seconds{{installation_id="{INSTALLATION}",period="today"}} 800
zaptec_sense_energy_complete{{installation_id="{INSTALLATION}",period="today",kind="total"}} 1
"""
PHASE_KEY = ("installation", INSTALLATION, "zaptec_sense_current_amperes", (("phase", "1"),))
POWER_KEY = ("charger", CHARGER, "zaptec_charge_power_watts", ())
ENERGY_KEY = ("installation", INSTALLATION, "zaptec_sense_total_energy_kwh", (("period", "today"),))


def test_values_zeroes_units_and_daily_energy_semantics():
    snapshot = parse_snapshot(HEADER + POINTS, now=1001)
    assert snapshot.points[POWER_KEY].value == 0
    assert snapshot.points[PHASE_KEY].value == 2.5
    assert snapshot.points[PHASE_KEY].metric.unit == "A"
    energy = snapshot.points[ENERGY_KEY]
    assert energy.value == 35.5 and energy.metric.unit == "kWh"
    assert not energy.metric.measurement
    key = ("installation", INSTALLATION, "zaptec_sense_reading_timestamp_seconds", ())
    assert snapshot.points[key].value == datetime.fromtimestamp(950, timezone.utc)


def test_source_health_is_independent_and_stale_is_not_success():
    snapshot = parse_snapshot(HEADER.replace("zaptec_exporter_up 1", "zaptec_exporter_up 0") + POINTS, now=1001)
    assert not snapshot.health["charger"].healthy
    assert snapshot.health["sense"].healthy and snapshot.health["history"].healthy
    stale = parse_snapshot(HEADER + POINTS, now=2000, max_age=900)
    assert not any(source.healthy for source in stale.health.values())
    future = parse_snapshot(HEADER.replace("1000", "2000"), now=1001)
    assert not any(source.healthy for source in future.health.values())


def test_initial_source_failure_still_provides_health_diagnostics():
    snapshot = parse_snapshot("zaptec_exporter_up 0\nzaptec_exporter_last_success_timestamp_seconds 0\n", now=1001)
    assert len(snapshot.points) == 1
    assert next(iter(snapshot.points.values())).value is False
    assert snapshot.health["charger"].timestamp is None


def test_history_coverage_can_legitimately_start_at_unix_epoch():
    snapshot = parse_snapshot(HEADER + f'zaptec_charge_history_from_timestamp_seconds{{charger_id="{CHARGER}"}} 0\n', now=1001)
    key = ("charger", CHARGER, "zaptec_charge_history_from_timestamp_seconds", ())
    assert snapshot.points[key].value == datetime(1970, 1, 1, tzinfo=timezone.utc)


@pytest.mark.parametrize("payload", ["prometheus_up 1\n", HEADER + POINTS + POINTS, "zaptec_exporter_up NaN\n"])
def test_invalid_or_duplicate_snapshot_is_rejected(payload):
    with pytest.raises(ValueError):
        parse_snapshot(payload, now=1001)


def test_unknown_labels_phases_and_nonfinite_values_do_not_create_entities():
    extra = f"""zaptec_sense_current_amperes{{installation_id="{INSTALLATION}",phase="4"}} 9
zaptec_sense_current_amperes{{installation_id="{INSTALLATION}",phase="2",token="private"}} 9
zaptec_sense_current_amperes{{installation_id="{INSTALLATION}",phase="3"}} NaN
zaptec_sense_total_energy_kwh{{installation_id="{INSTALLATION}",period="arbitrary-date"}} 999
"""
    assert parse_snapshot(HEADER + POINTS + extra, now=1001) == parse_snapshot(HEADER + POINTS, now=1001)


@pytest.mark.parametrize("url", ["http://host:8080", "http://host:8080/", "http://host:8080/metrics"])
def test_normalizes_endpoint(url):
    assert normalize_url(url) == "http://host:8080/metrics"


@pytest.mark.parametrize("url", ["file:///config/secret", "http://user:secret@host", "http://host/?token=secret", "http://host/probe", "http://host:bad", "http://host/#fragment"])
def test_rejects_unsupported_urls(url):
    with pytest.raises(ValueError):
        normalize_url(url)
