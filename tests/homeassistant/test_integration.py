"""Exercise real Home Assistant entities and local-only coordinator fetching."""
import asyncio
from dataclasses import replace
from types import MappingProxyType
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant

from custom_components.zaptec_exporter.binary_sensor import StatusSensor
from custom_components.zaptec_exporter.coordinator import ExporterCoordinator, MAX_RESPONSE_BYTES, fetch_snapshot
from custom_components.zaptec_exporter.data import parse_snapshot
from custom_components.zaptec_exporter.entity import setup_entities
from custom_components.zaptec_exporter.sensor import MetricSensor

from test_data import HEADER, POINTS, POWER_KEY, PHASE_KEY, ENERGY_KEY


def entry():
    return ConfigEntry(domain="zaptec_exporter", title="Test", version=1, minor_version=1,
                       data={"url": "http://exporter:8080/metrics", "max_age": 900}, options={},
                       source="user", unique_id="exporter", discovery_keys=MappingProxyType({}), subentries_data=[])


@pytest.mark.asyncio
async def test_independent_availability_transport_failure_and_recovery(tmp_path):
    hass = HomeAssistant(str(tmp_path))
    coordinator = ExporterCoordinator(hass, entry())
    snapshot = parse_snapshot(HEADER + POINTS, now=1001)
    coordinator.async_set_updated_data(snapshot)
    charger = MetricSensor(coordinator, POWER_KEY, snapshot.points[POWER_KEY])
    phase = MetricSensor(coordinator, PHASE_KEY, snapshot.points[PHASE_KEY])
    energy = MetricSensor(coordinator, ENERGY_KEY, snapshot.points[ENERGY_KEY])
    health_key = ("exporter", "exporter", "zaptec_exporter_up", ())
    health = StatusSensor(coordinator, health_key, snapshot.points[health_key])
    assert charger.available and phase.available and health.is_on
    assert phase.native_unit_of_measurement == "A" and phase.state_class == "measurement"
    assert energy.device_class == "energy" and energy.state_class is None
    assert "day_start" in energy.extra_state_attributes
    degraded = parse_snapshot(HEADER.replace("zaptec_exporter_up 1", "zaptec_exporter_up 0") + POINTS, now=1001)
    coordinator.async_set_updated_data(degraded)
    assert not charger.available and phase.available
    assert health.available and health.is_on is False
    with patch("custom_components.zaptec_exporter.coordinator.fetch_snapshot", new=AsyncMock(side_effect=ValueError("invalid"))):
        await coordinator.async_refresh()
    assert not phase.available and not health.available
    with patch("custom_components.zaptec_exporter.coordinator.fetch_snapshot", new=AsyncMock(return_value=snapshot)):
        await coordinator.async_refresh()
    assert phase.available and charger.available and health.is_on
    without_phase = replace(snapshot, points={k: v for k, v in snapshot.points.items() if k != PHASE_KEY})
    coordinator.async_set_updated_data(without_phase)
    assert not phase.available
    await coordinator.async_shutdown()
    await hass.async_stop()


@pytest.mark.asyncio
async def test_dynamic_discovery_without_duplicate_ids(tmp_path):
    hass = HomeAssistant(str(tmp_path))
    config_entry = entry()
    coordinator = ExporterCoordinator(hass, config_entry)
    config_entry.runtime_data = coordinator
    initial = parse_snapshot(HEADER, now=1001)
    coordinator.async_set_updated_data(initial)
    added = []
    setup_entities(config_entry, lambda entities: added.extend(entities), MetricSensor, False)
    count = len(added)
    complete = parse_snapshot(HEADER + POINTS, now=1001)
    coordinator.async_set_updated_data(complete)
    assert len(added) > count
    count = len(added)
    ids = {entity.unique_id for entity in added}
    coordinator.async_set_updated_data(complete)
    assert len(added) == count == len(ids)
    # Recreating an entity after reload preserves its identity.
    phase = MetricSensor(coordinator, PHASE_KEY, complete.points[PHASE_KEY])
    assert phase.unique_id in ids
    assert ("zaptec_exporter", f"installation:{PHASE_KEY[1]}") in phase.device_info["identifiers"]
    await coordinator.async_shutdown()
    await hass.async_stop()


@pytest.mark.asyncio
async def test_local_fetch_disables_redirects_and_reads_until_eof(tmp_path):
    hass = HomeAssistant(str(tmp_path))
    response = MagicMock()
    response.status = 200
    response.content.readexactly = AsyncMock(side_effect=asyncio.IncompleteReadError(HEADER.encode(), MAX_RESPONSE_BYTES + 1))
    session = MagicMock()
    session.get.return_value.__aenter__ = AsyncMock(return_value=response)
    session.get.return_value.__aexit__ = AsyncMock(return_value=None)
    with patch("custom_components.zaptec_exporter.coordinator.async_get_clientsession", return_value=session):
        result = await fetch_snapshot(hass, "http://exporter:8080", 900)
    assert "charger" in result.health
    session.get.assert_called_once_with("http://exporter:8080/metrics", allow_redirects=False)
    response.content.readexactly.assert_awaited_once_with(MAX_RESPONSE_BYTES + 1)
    response.content.readexactly = AsyncMock(return_value=b"x" * (MAX_RESPONSE_BYTES + 1))
    with patch("custom_components.zaptec_exporter.coordinator.async_get_clientsession", return_value=session), pytest.raises(ValueError):
        await fetch_snapshot(hass, "http://exporter:8080", 900)
    await hass.async_stop()


@pytest.mark.asyncio
async def test_config_flow_normalizes_url_and_returns_useful_errors(tmp_path):
    from custom_components.zaptec_exporter.config_flow import ConfigFlow
    hass = HomeAssistant(str(tmp_path))
    flow = ConfigFlow()
    flow.hass = hass
    with patch.object(flow, "async_set_unique_id", new=AsyncMock()) as unique, \
         patch.object(flow, "_abort_if_unique_id_configured"), \
         patch.object(flow, "async_create_entry", return_value={"type":"create_entry"}) as create, \
         patch("custom_components.zaptec_exporter.config_flow.fetch_snapshot", new=AsyncMock()) as fetch:
        result = await flow.async_step_user({"url":"http://exporter:8080/", "max_age":900})
        assert result["type"] == "create_entry"
        assert create.call_args.kwargs["data"]["url"] == "http://exporter:8080/metrics"
        unique.assert_awaited_once_with("http://exporter:8080/metrics")
        fetch.assert_awaited_once_with(hass,"http://exporter:8080/metrics",900)
    with patch.object(flow,"async_show_form",return_value={"type":"form"}) as form, \
         patch("custom_components.zaptec_exporter.config_flow.fetch_snapshot",new=AsyncMock(side_effect=ValueError("bad response"))):
        await flow.async_step_user({"url":"http://exporter:8080", "max_age":900})
        assert form.call_args.kwargs["errors"] == {"base":"cannot_connect"}
    await hass.async_stop()
