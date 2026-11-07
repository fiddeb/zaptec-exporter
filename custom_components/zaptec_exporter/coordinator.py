"""One local HTTP fetch for all entities; no Zaptec or Grafana credentials."""
import asyncio
from datetime import timedelta
import logging

import aiohttp
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from .data import normalize_url, parse_snapshot

MAX_RESPONSE_BYTES = 2 * 1024 * 1024


async def fetch_snapshot(hass, url, max_age):
    url = normalize_url(url)
    async with asyncio.timeout(15):
        async with async_get_clientsession(hass).get(url, allow_redirects=False) as response:
            if response.status != 200:
                raise ValueError("Exporter HTTP request failed")
            try:
                payload = await response.content.readexactly(MAX_RESPONSE_BYTES + 1)
            except asyncio.IncompleteReadError as err:
                payload = err.partial
            else:
                raise ValueError("Exporter response is too large")
            return parse_snapshot(payload.decode("utf-8"), max_age)


class ExporterCoordinator(DataUpdateCoordinator):
    def __init__(self, hass, entry):
        super().__init__(hass, logging.getLogger(__name__), name="Zaptec Exporter",
                         config_entry=entry, update_interval=timedelta(seconds=30))
        self.entry = entry

    async def _async_update_data(self):
        try:
            return await fetch_snapshot(self.hass, self.entry.data["url"], self.entry.data["max_age"])
        except (aiohttp.ClientError, TimeoutError, ValueError) as err:
            raise UpdateFailed("Exporter unavailable or invalid") from err
