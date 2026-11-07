"""Decode the same cached /metrics snapshot consumed by Alloy."""
from dataclasses import dataclass
from datetime import datetime, timezone
import math
import time
from urllib.parse import urlsplit
from uuid import UUID

from prometheus_client.parser import text_string_to_metric_families

from .catalogue import METRICS, PERIOD_METRICS, PHASE_METRICS, SOURCES, Metric

type PointKey = tuple[str, str, str, tuple[tuple[str, str], ...]]


@dataclass(frozen=True)
class Health:
    healthy: bool
    timestamp: float | None


@dataclass(frozen=True)
class Point:
    name: str
    value: float | bool | datetime
    metric: Metric


@dataclass(frozen=True)
class Snapshot:
    points: dict[PointKey, Point]
    health: dict[str, Health]


def normalize_url(value: str) -> str:
    url = urlsplit(value.strip())
    # Access the port to reject malformed values before requesting anything.
    _ = url.port
    if url.scheme not in ("http", "https") or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/", "/metrics"):
        raise ValueError("Use an HTTP(S) exporter URL without credentials or query parameters")
    return f"{url.scheme}://{url.netloc}/metrics"


def timestamp(value: float) -> datetime | None:
    if not math.isfinite(value):
        return None
    try:
        return datetime.fromtimestamp(value, timezone.utc)
    except (ValueError, OverflowError, OSError):
        return None


def parse_snapshot(text: str, max_age: int = 900, now: float | None = None) -> Snapshot:
    now = time.time() if now is None else now
    samples = [sample for family in text_string_to_metric_families(text) for sample in family.samples]
    scalars = {}
    for sample in samples:
        if not sample.labels:
            if sample.name in scalars:
                raise ValueError("Duplicate exporter health sample")
            scalars[sample.name] = sample.value
    points, health = {}, {}
    for source, (up, stamp) in SOURCES.items():
        if up not in scalars:
            continue
        value = scalars[up]
        if value not in (0, 1):
            raise ValueError("Invalid exporter health sample")
        last_success = scalars.get(stamp)
        valid_time = last_success is not None and last_success > 0 and timestamp(last_success) is not None
        healthy = value == 1 and valid_time and 0 <= now - last_success <= max_age
        health[source] = Health(healthy, last_success if valid_time else None)
        definition = Metric(f"{source.title()} API healthy", source=source, scope="exporter", binary=True, diagnostic=True, device_class="connectivity")
        points[("exporter", "exporter", up, ())] = Point(definition.title, healthy, definition)
        if valid_time:
            definition = Metric(f"{source.title()} last success", source=source, scope="exporter", device_class="timestamp", diagnostic=True)
            points[("exporter", "exporter", stamp, ())] = Point(definition.title, timestamp(last_success), definition)
    if not health:
        raise ValueError("Not a Zaptec exporter snapshot")

    for sample in samples:
        definition = METRICS.get(sample.name)
        if definition is None or not math.isfinite(sample.value):
            continue
        labels = sample.labels
        id_label = "installation_id" if definition.scope == "installation" else "charger_id"
        expected = {id_label}
        dimensions = []
        title = definition.title
        if sample.name in PHASE_METRICS:
            if labels.get("phase") not in ("1", "2", "3"):
                continue
            expected.add("phase")
            dimensions.append(("phase", labels["phase"]))
            title += f" phase {labels['phase']}"
        if sample.name in PERIOD_METRICS:
            if labels.get("period") not in ("today", "yesterday"):
                continue
            expected.add("period")
            dimensions.append(("period", labels["period"]))
            title += f" {labels['period']}"
        if sample.name == "zaptec_sense_energy_complete":
            if labels.get("kind") not in ("total", "charge"):
                continue
            expected.add("kind")
            dimensions.append(("kind", labels["kind"]))
            title += f" ({labels['kind']})"
        if set(labels) != expected:
            continue
        try:
            device_id = str(UUID(labels[id_label]))
        except (ValueError, KeyError):
            continue
        value = sample.value
        if definition.binary:
            if value not in (0, 1):
                continue
            value = value == 1
        elif definition.device_class == "timestamp":
            value = timestamp(value)
            if value is None:
                continue
        key = (definition.scope, device_id, sample.name, tuple(sorted(dimensions)))
        if key in points:
            raise ValueError("Duplicate measurement sample")
        points[key] = Point(title, value, definition)
    return Snapshot(points, health)
