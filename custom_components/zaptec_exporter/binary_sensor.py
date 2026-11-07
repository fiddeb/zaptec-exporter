"""Source health, connectivity and hourly coverage diagnostics."""
from homeassistant.components.binary_sensor import BinarySensorEntity

from .entity import ExporterEntity, setup_entities


async def async_setup_entry(hass, entry, async_add_entities):
    setup_entities(entry, async_add_entities, StatusSensor, True)


class StatusSensor(ExporterEntity, BinarySensorEntity):
    @property
    def is_on(self):
        return self.point.value if self.point is not None else None

    @property
    def device_class(self):
        return self.definition.device_class
