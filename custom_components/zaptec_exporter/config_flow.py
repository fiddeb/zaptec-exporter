"""Configure access to a local exporter without Zaptec credentials."""
import aiohttp
import voluptuous as vol
from homeassistant import config_entries

from .const import DEFAULT_MAX_AGE, DEFAULT_URL, DOMAIN
from .coordinator import fetch_snapshot
from .data import normalize_url


class ConfigFlow(config_entries.ConfigFlow, domain=DOMAIN):
    VERSION = 1

    async def async_step_user(self, user_input=None):
        errors = {}
        if user_input is not None:
            try:
                url = normalize_url(user_input["url"])
                await fetch_snapshot(self.hass, url, user_input["max_age"])
            except (aiohttp.ClientError, TimeoutError, ValueError):
                errors["base"] = "cannot_connect"
            else:
                await self.async_set_unique_id(url)
                self._abort_if_unique_id_configured()
                return self.async_create_entry(title="Zaptec Exporter", data={**user_input, "url": url})
        return self.async_show_form(step_id="user", data_schema=vol.Schema({
            vol.Required("url", default=DEFAULT_URL): str,
            vol.Required("max_age", default=DEFAULT_MAX_AGE): vol.All(vol.Coerce(int), vol.Range(min=60, max=259200)),
        }), errors=errors)
