"""Explicit metric semantics; energy gauges are not cumulative counters."""
from dataclasses import dataclass
from typing import Literal


@dataclass(frozen=True)
class Metric:
    title: str
    source: Literal["charger", "history", "sense"] = "charger"
    scope: Literal["charger", "installation", "exporter"] = "charger"
    unit: str | None = None
    device_class: str | None = None
    measurement: bool = False
    binary: bool = False
    diagnostic: bool = False


METRICS = {
    "zaptec_charge_power_watts": Metric("Charge power", unit="W", device_class="power", measurement=True),
    "zaptec_charge_session_energy_kwh": Metric("Session energy", unit="kWh", device_class="energy"),
    "zaptec_charge_current_amperes": Metric("Charge current", unit="A", device_class="current", measurement=True),
    "zaptec_charge_voltage_volts": Metric("Voltage", unit="V", device_class="voltage", measurement=True),
    "zaptec_charge_current_limit_amperes": Metric("Current limit", unit="A", device_class="current", measurement=True),
    "zaptec_charger_operation_mode": Metric("Operation mode"),
    "zaptec_charger_online": Metric("Online", device_class="connectivity", binary=True),
    "zaptec_charge_phases_mode": Metric("Phases mode"),
    "zaptec_charge_completed_energy_kwh": Metric("Completed session energy", source="history", unit="kWh", device_class="energy"),
    "zaptec_charge_completed_sessions": Metric("Completed sessions", source="history"),
    "zaptec_charge_history_from_timestamp_seconds": Metric("History coverage start", source="history", device_class="timestamp", diagnostic=True),
    "zaptec_charge_last_session_energy_kwh": Metric("Last session energy", source="history", unit="kWh", device_class="energy"),
    "zaptec_charge_last_session_end_timestamp_seconds": Metric("Last session ended", source="history", device_class="timestamp"),
    "zaptec_charge_last_session_duration_seconds": Metric("Last session duration", source="history", unit="s", device_class="duration", measurement=True),
    "zaptec_sense_current_amperes": Metric("Current", source="sense", scope="installation", unit="A", device_class="current", measurement=True),
    "zaptec_sense_voltage_volts": Metric("Voltage", source="sense", scope="installation", unit="V", device_class="voltage", measurement=True),
    "zaptec_sense_charge_current_amperes": Metric("Charge current", source="sense", scope="installation", unit="A", device_class="current", measurement=True),
    "zaptec_sense_available_current_amperes": Metric("Available current", source="sense", scope="installation", unit="A", device_class="current", measurement=True),
    "zaptec_sense_neutral_current_amperes": Metric("Neutral current", source="sense", scope="installation", unit="A", device_class="current", measurement=True),
    "zaptec_sense_power_watts": Metric("Power", source="sense", scope="installation", unit="W", device_class="power", measurement=True),
    "zaptec_sense_power_limit_watts": Metric("Power limit", source="sense", scope="installation", unit="W", device_class="power", measurement=True),
    "zaptec_sense_dynamic_power_limit_watts": Metric("Dynamic power limit", source="sense", scope="installation", unit="W", device_class="power", measurement=True),
    "zaptec_sense_total_energy_kwh": Metric("Total energy", source="sense", scope="installation", unit="kWh", device_class="energy"),
    "zaptec_sense_charge_energy_kwh": Metric("Charging energy", source="sense", scope="installation", unit="kWh", device_class="energy"),
    "zaptec_sense_reading_timestamp_seconds": Metric("Phase reading time", source="sense", scope="installation", device_class="timestamp", diagnostic=True),
    "zaptec_sense_power_reading_timestamp_seconds": Metric("Power reading time", source="sense", scope="installation", device_class="timestamp", diagnostic=True),
    "zaptec_sense_energy_as_of_timestamp_seconds": Metric("Energy retrieval time", source="sense", scope="installation", device_class="timestamp", diagnostic=True),
    "zaptec_sense_day_start_timestamp_seconds": Metric("Day start", source="sense", scope="installation", device_class="timestamp", diagnostic=True),
    "zaptec_sense_energy_complete": Metric("Energy coverage complete", source="sense", scope="installation", binary=True, diagnostic=True),
}

# Labels are constrained so untrusted or future dimensions cannot create
# unbounded entities or accidentally identify a different physical device.
PHASE_METRICS = {
    "zaptec_charge_current_amperes", "zaptec_charge_voltage_volts",
    "zaptec_sense_current_amperes", "zaptec_sense_voltage_volts",
    "zaptec_sense_charge_current_amperes", "zaptec_sense_available_current_amperes",
}
PERIOD_METRICS = {
    "zaptec_sense_total_energy_kwh", "zaptec_sense_charge_energy_kwh",
    "zaptec_sense_day_start_timestamp_seconds", "zaptec_sense_energy_complete",
}
SOURCES = {
    "charger": ("zaptec_exporter_up", "zaptec_exporter_last_success_timestamp_seconds"),
    "history": ("zaptec_exporter_history_up", "zaptec_exporter_history_last_success_timestamp_seconds"),
    "sense": ("zaptec_exporter_sense_up", "zaptec_exporter_sense_last_success_timestamp_seconds"),
}
