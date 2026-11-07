"""Local measurement sensors and energy gauges with verified units."""
from homeassistant.components.sensor import SensorEntity, SensorStateClass

from .entity import ExporterEntity, setup_entities


async def async_setup_entry(hass, entry, async_add_entities):
    setup_entities(entry, async_add_entities, MetricSensor, False)


class MetricSensor(ExporterEntity, SensorEntity):
    @property
    def native_value(self):
        return self.point.value if self.point is not None else None

    @property
    def native_unit_of_measurement(self):
        return self.definition.unit

    @property
    def device_class(self):
        return self.definition.device_class

    @property
    def state_class(self):
        # Daily, session and corrected archive energy are gauges. Never turn
        # them into increasing meters or fabricate Energy dashboard statistics.
        return SensorStateClass.MEASUREMENT if self.definition.measurement else None
