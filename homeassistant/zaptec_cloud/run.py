"""Run a file-configured exporter and optional Alloy in a Supervisor app."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
from urllib.parse import urlparse


def text(options, key, default=None):
    value = options.get(key, default)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"Configure {key}")
    return value


def boolean(options, key, default):
    value = options.get(key, default)
    if type(value) is not bool:
        raise ValueError(f"Invalid {key}")
    return value


def seconds(options, key, default, minimum=300, maximum=86400):
    value = options.get(key, default)
    if type(value) is not int or not minimum <= value <= maximum:
        raise ValueError(f"Invalid {key}")
    return value


def child_environment():
    # File config owns exporter settings; do not inherit accidental overrides
    # or pass either service's credentials to the other service.
    return {
        key: value for key, value in os.environ.items()
        if not key.startswith(("ZAPTEC_", "GRAFANA_CLOUD_"))
        and key not in ("LISTEN_ADDR", "SUPERVISOR_TOKEN")
    }


def settings(options, data_dir):
    if not isinstance(options, dict):
        raise ValueError("Options must be an object")
    poll = seconds(options, "poll_interval", 300)
    config = {
        "username": text(options, "username"),
        "password": text(options, "password"),
        "charger_id": text(options, "charger_id"),
        "mode": "rest",
        "listen_addr": ":8080",
        "poll_interval": f"{poll}s",
        "stale_after": f"{2 * poll}s",
        "history_enabled": boolean(options, "history_enabled", True),
        "history_from": text(options, "history_from", "1970-01-01T00:00:00Z"),
        "history_interval": f"{seconds(options, 'history_interval', 300)}s",
        "history_file": str(data_dir / "zaptec-history.json"),
        "sense_enabled": boolean(options, "sense_enabled", False),
        "sense_interval": f"{seconds(options, 'sense_interval', 300)}s",
        "sense_timezone": text(options, "sense_timezone", "Europe/Stockholm"),
    }
    if not boolean(options, "cloud_enabled", True):
        return config, None
    endpoint = text(options, "metrics_url")
    url = urlparse(endpoint)
    if url.scheme != "https" or not url.hostname or url.username or url.password:
        raise ValueError("metrics_url must be HTTPS without embedded credentials")
    alloy_env = child_environment()
    alloy_env.update(
        GRAFANA_CLOUD_METRICS_URL=endpoint,
        GRAFANA_CLOUD_METRICS_USER=text(options, "metrics_user"),
        GRAFANA_CLOUD_METRICS_TOKEN=text(options, "metrics_token"),
        ZAPTEC_INSTANCE=text(options, "instance", "homeassistant"),
        ZAPTEC_SCRAPE_INTERVAL=f"{seconds(options, 'scrape_interval', 60, 15, 3600)}s",
    )
    return config, alloy_env


def main(data_dir=Path("/data")):
    os.umask(0o077)
    try:
        options = json.loads((data_dir / "options.json").read_text())
        config, alloy_env = settings(options, data_dir)
        config_path = data_dir / "exporter-config.json"
        # Set permissions even if a previous version left a broader mode.
        fd = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as output:
            os.fchmod(output.fileno(), 0o600)
            json.dump(config, output)
    except (ValueError, OSError):
        # JSON/URL errors can contain option text; do not log exception bodies.
        print("Invalid app configuration: check Zaptec credentials, options and enabled Cloud settings", flush=True)
        return 1

    stopping = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopping.set())
    signal.signal(signal.SIGINT, lambda *_: stopping.set())
    children = []
    failed = False
    try:
        children.append(subprocess.Popen(
            ["zaptec-exporter", "-config", str(config_path)], env=child_environment()))
        if alloy_env is not None:
            children.append(subprocess.Popen([
                "alloy", "run", "--server.http.listen-addr=127.0.0.1:12345",
                "--storage.path=" + str(data_dir / "alloy"), "/etc/zaptec/config.alloy",
            ], env=alloy_env))
        while not stopping.wait(1):
            if any(child.poll() is not None for child in children):
                print("A telemetry process stopped; inspect the app logs and restart", flush=True)
                failed = True
                break
    except OSError:
        print("Unable to start telemetry processes", flush=True)
        failed = True
    finally:
        for child in children:
            if child.poll() is None:
                child.terminate()
        for child in children:
            try:
                child.wait(timeout=15)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
    return int(failed)


if __name__ == "__main__":
    sys.exit(main())
