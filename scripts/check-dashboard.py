"""Validate generated artifacts and optionally emit executable PromQL tests."""
import argparse
import importlib.util
import json
from pathlib import Path
import re
from xml.etree import ElementTree

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("dashboard", ROOT / "scripts/build-dashboard.py")
dashboard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dashboard)


def validate(builder):
    for name, expected in (("zaptec-charging.dynamic.json", dashboard.dynamic(builder)),
                           ("zaptec-charging.json", dashboard.classic(builder))):
        assert json.loads((ROOT / "dashboards" / name).read_text()) == expected, f"Regenerate {name}"
    metric_contract = "\n".join(p.read_text() for p in ROOT.glob("*.go") if not p.name.endswith("_test.go"))
    for panel in builder.panels:
        for query in panel["targets"]:
            expr = query["expr"]
            for metric in re.findall(r"\bzaptec_[a-z_]+", expr):
                assert metric in metric_contract, f"Unknown metric {metric}"
            assert not re.search(r"\b(rate|increase|sum_over_time)\s*\(", expr), expr
            assert "vector(0)" not in expr, "Missing observations must remain absent"
            if panel["type"] in ("stat", "canvas"):
                assert query["instant"] and not query["range"], "Cards must not revive an old sample"
        if panel["type"] == "canvas":
            background = panel["options"]["root"]["elements"][0]["background"]
            uri = background["image"]["fixed"]
            # Grafana's getPublicOrAbsoluteUrl checks for :/ and prefixes other
            # strings with public/build/. In particular, base64 data: URLs fail.
            assert ":/" in uri and uri.startswith("https://"), "Canvas requires an absolute HTTPS image URL"
            assert re.fullmatch(r"https://raw\.githubusercontent\.com/fiddeb/zaptec-exporter/[0-9a-f]{40}/dashboards/assets/charging-scene\.svg", uri), "Pin the published SVG revision"
            scene = ElementTree.parse(ROOT / "dashboards/assets/charging-scene.svg").getroot()
            assert scene.attrib["viewBox"] == "0 0 1200 650"
            names = {q["legendFormat"] for q in panel["targets"]}
            for element in panel["options"]["root"]["elements"]:
                if element["type"] == "icon":
                    assert element["config"]["fill"]["field"] == "Charging state"
                    assert element["config"]["fill"]["fixed"] == "#66747D"
                    assert re.fullmatch(r"https://raw\.githubusercontent\.com/fiddeb/zaptec-exporter/[0-9a-f]{40}/dashboards/assets/charging-flow\.svg", element["config"]["path"]["fixed"]), "Pin the published flow layer revision"
                    continue
                text = element["config"]["text"]
                if text["mode"] == "field":
                    assert text["field"] in names, f"Unbound canvas field {text['field']}"
                    assert element["type"] == "text", "Metric-value ignores missing-field fallback"
                    assert text["fixed"] in ("—", "Status saknas"), "Missing fields need explicit fallback text"
    for tab in builder.tabs:
        positions = [p["gridPos"] for p in tab["panels"]]
        for i, rect in enumerate(positions):
            assert rect["x"] >= 0 and rect["y"] >= 0 and rect["x"] + rect["w"] <= 24
            for other in positions[i+1:]:
                overlap = (rect["x"] < other["x"] + other["w"] and other["x"] < rect["x"] + rect["w"]
                           and rect["y"] < other["y"] + other["h"] and other["y"] < rect["y"] + rect["h"])
                assert not overlap, f"Overlapping panels in {tab['title']}"


def resolved(expression):
    for variable in ("instance", "charger", "installation"):
        expression = expression.replace("${" + variable + ":regex}", ".*")
    return expression


def promtool_tests(builder):
    labels = 'job="zaptec",instance="test"'
    car_labels = labels + ',charger_id="test-charger"'
    expression = resolved(dashboard.current("zaptec_charge_power_watts", "charger", "charger", 1000))
    cases = []
    # A genuine zero is data; a missing/stale observation or failed fetch is not.
    for name, samples, source_up, scrape_up, expected in (
        ("zero", "0", 1, 1, 0), ("charging", "7400", 1, 1, 7.4),
        ("missing", None, 1, 1, None), ("fetch-failed", "7400", 0, 1, None),
        ("scrape-failed", "7400", 1, 0, None),
    ):
        inputs = [{"series": f"zaptec_exporter_up{{{labels}}}", "values": str(source_up)},
                  {"series": f"up{{{labels}}}", "values": str(scrape_up)}]
        if samples is not None:
            inputs.append({"series": f"zaptec_charge_power_watts{{{car_labels}}}", "values": samples})
        cases.append({"name": name, "input_series": inputs, "promql_expr_test": [{"expr": expression,
            "eval_time": "0m", "exp_samples": [] if expected is None else [{"labels": "{" + car_labels + "}", "value": expected}]}]})
    for name, mode, power, source_up, scrape_up, expected in (
        ("charging-mode", 3, 7400, 1, 1, 1), ("waiting-mode", 2, 0, 1, 1, 0),
        ("mode-precedes-power", 2, 7400, 1, 1, 0),
        ("power-fallback", None, 7400, 1, 1, 1), ("zero-power", None, 0, 1, 1, 0),
        ("unknown-status", None, None, 1, 1, -1), ("failed-charger", 3, 7400, 0, 1, -1),
        ("failed-scrape", 3, 7400, 1, 0, -1),
    ):
        inputs = [{"series": f"zaptec_exporter_up{{{labels}}}", "values": str(source_up)},
                  {"series": f"up{{{labels}}}", "values": str(scrape_up)},
                  # The house consuming power must never turn the charging cable green.
                  {"series": f'zaptec_sense_power_watts{{{labels},installation_id="test-installation"}}', "values": "9000"}]
        for metric, value in (("zaptec_charger_operation_mode", mode), ("zaptec_charge_power_watts", power)):
            if value is not None:
                inputs.append({"series": f"{metric}{{{car_labels}}}", "values": str(value)})
        cases.append({"name": name, "input_series": inputs, "promql_expr_test": [{"expr": resolved(dashboard.charging_state()),
            "eval_time": "0m", "exp_samples": [{"labels": "{}", "value": expected}]}]})
    # Every panel expression is parsed and evaluated by Prometheus, even without matching series.
    cases.append({"name": "all-panel-queries", "promql_expr_test": [
        {"expr": expr, "eval_time": "0m", "exp_samples": [{"labels": "{}", "value": -1}] if expr == resolved(dashboard.charging_state()) else []}
        for expr in sorted({resolved(q["expr"]) for p in builder.panels for q in p["targets"]})]})
    return {"rule_files": [], "evaluation_interval": "1m", "tests": cases}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--promtool-test", type=Path)
    args = parser.parse_args()
    builder = dashboard.build()
    validate(builder)
    if args.promtool_test:
        args.promtool_test.write_text(json.dumps(promtool_tests(builder), ensure_ascii=False, indent=2) + "\n")
    print(f"Validated {len(builder.panels)} panels and {len(builder.tabs)} layouts in both formats")
