"""Stable entities grouped by charger, installation and exporter."""
from homeassistant.core import callback
from homeassistant.helpers.entity import DeviceInfo, EntityCategory
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN
from .data import timestamp


@callback
def setup_entities(entry, async_add_entities, entity_type, binary):
    coordinator = entry.runtime_data
    known = set()

    @callback
    def discover():
        new = []
        for key, point in coordinator.data.points.items():
            if point.metric.binary == binary and key not in known:
                known.add(key)
                new.append(entity_type(coordinator, key, point))
        if new:
            async_add_entities(new)

    discover()
    entry.async_on_unload(coordinator.async_add_listener(discover))


class ExporterEntity(CoordinatorEntity):
    _attr_has_entity_name = True

    def __init__(self, coordinator, key, point):
        super().__init__(coordinator)
        self.key = key
        self.definition = point.metric
        scope, device_id, metric, dimensions = key
        identity = coordinator.entry.entry_id if scope == "exporter" else device_id
        self._attr_unique_id = ":".join((scope, identity, metric, *(f"{k}={v}" for k, v in dimensions)))
        self._attr_name = point.name
        if point.metric.diagnostic:
            self._attr_entity_category = EntityCategory.DIAGNOSTIC
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, f"{scope}:{identity}")},
            name={"charger": "Zaptec charger", "installation": "Zaptec Sense", "exporter": "Zaptec Exporter"}[scope],
            manufacturer="Zaptec" if scope != "exporter" else "fiddeb",
            model={"charger": "Charger", "installation": "Sense / installation", "exporter": "Exporter"}[scope],
        )

    @property
    def point(self):
        return self.coordinator.data.points.get(self.key)

    @property
    def available(self):
        if not super().available or self.point is None:
            return False
        if self.definition.diagnostic:
            return True
        source = self.coordinator.data.health.get(self.definition.source)
        return source is not None and source.healthy

    @property
    def extra_state_attributes(self):
        attributes = {"source": self.definition.source, "metric": self.key[2], **dict(self.key[3])}
        health = self.coordinator.data.health.get(self.definition.source)
        if health is not None and health.timestamp is not None:
            attributes["last_success"] = timestamp(health.timestamp).isoformat()
        if "period" in attributes:
            day_key = (self.key[0], self.key[1], "zaptec_sense_day_start_timestamp_seconds", (("period", attributes["period"]),))
            if day := self.coordinator.data.points.get(day_key):
                attributes["day_start"] = day.value.isoformat()
        return attributes
