import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import MagicMock, patch

import run


ROOT = Path(__file__).resolve().parents[2]


class AppTests(unittest.TestCase):
    def options(self, cloud=True):
        options = dict(username="user", password="zaptec-secret", charger_id="charger",
                       cloud_enabled=cloud)
        if cloud:
            options.update(metrics_url="https://metrics.example/api/prom/push",
                           metrics_user="12345", metrics_token="metrics-secret")
        return options

    def test_exporter_only_defaults_and_persistence(self):
        config, alloy = run.settings(self.options(False), Path("/data"))
        self.assertIsNone(alloy)
        self.assertEqual(config["poll_interval"], "300s")
        self.assertEqual(config["history_interval"], "300s")
        self.assertEqual(config["sense_interval"], "300s")
        self.assertEqual(config["stale_after"], "600s")
        self.assertFalse(config["sense_enabled"])
        self.assertEqual(config["history_file"], "/data/zaptec-history.json")

    def test_credentials_are_separated_and_not_inherited(self):
        with patch.dict(os.environ, {
            "ZAPTEC_PASSWORD": "old-secret", "GRAFANA_CLOUD_METRICS_TOKEN": "old-cloud-secret",
            "GRAFANA_CLOUD_LOKI_TOKEN": "unneeded-log-secret", "SUPERVISOR_TOKEN": "ha-secret",
            "LISTEN_ADDR": ":9000",
        }):
            exporter_env = run.child_environment()
            config, alloy = run.settings(self.options(), Path("/data"))
        self.assertEqual(config["password"], "zaptec-secret")
        self.assertEqual(alloy["GRAFANA_CLOUD_METRICS_TOKEN"], "metrics-secret")
        self.assertEqual(alloy["ZAPTEC_SCRAPE_INTERVAL"], "60s")
        self.assertNotIn("ZAPTEC_PASSWORD", alloy)
        self.assertNotIn("GRAFANA_CLOUD_LOKI_TOKEN", alloy)
        self.assertNotIn("SUPERVISOR_TOKEN", alloy)
        self.assertNotIn("LISTEN_ADDR", exporter_env)
        self.assertNotIn("GRAFANA_CLOUD_METRICS_TOKEN", exporter_env)

    def test_poll_change_keeps_freshness_grace(self):
        options = self.options(False)
        options.update(poll_interval=900, history_interval=1200, sense_interval=600,
                       sense_enabled=True, sense_timezone="Europe/Stockholm")
        config, _ = run.settings(options, Path("/data"))
        self.assertEqual(config["stale_after"], "1800s")
        self.assertEqual(config["history_interval"], "1200s")
        self.assertEqual(config["sense_interval"], "600s")
        self.assertTrue(config["sense_enabled"])

    def test_invalid_options(self):
        changes = (
            dict(username=""), dict(password=""), dict(charger_id=""),
            dict(poll_interval=299), dict(poll_interval=True), dict(poll_interval="300"),
            dict(history_interval=0), dict(sense_interval=299), dict(sense_enabled="false"),
            dict(cloud_enabled="false"), dict(history_enabled=1), dict(metrics_token=""),
            dict(metrics_url="http://example.com"), dict(metrics_url="https://user:secret@example.com"),
            dict(scrape_interval=1), dict(instance=""), dict(sense_timezone=""),
        )
        for change in changes:
            with self.subTest(field=list(change)):
                options = self.options()
                options.update(change)
                with self.assertRaises(ValueError):
                    run.settings(options, Path("/data"))

    def invoke(self, directory, options, processes, stop=True):
        (directory / "options.json").write_text(json.dumps(options))
        with patch("run.os.umask"), patch("run.signal.signal"), \
             patch("run.threading.Event") as event, \
             patch("run.subprocess.Popen", side_effect=processes) as popen:
            event.return_value.wait.return_value = stop
            code = run.main(directory)
        return code, popen

    def test_process_selection_and_private_file_config(self):
        for enabled in (False, True):
            with self.subTest(cloud=enabled), tempfile.TemporaryDirectory() as temp:
                directory = Path(temp)
                exporter, alloy = MagicMock(), MagicMock()
                exporter.poll.return_value = alloy.poll.return_value = None
                options = self.options(enabled)
                options["password"] = 'literal "password" with \\ and\nnewline'
                path = directory / "exporter-config.json"
                path.write_text("old")
                path.chmod(0o644)
                code, popen = self.invoke(directory, options, [exporter, alloy])
                self.assertEqual(code, 0)
                self.assertEqual(popen.call_count, 2 if enabled else 1)
                self.assertEqual(popen.call_args_list[0].args[0],
                                 ["zaptec-exporter", "-config", str(path)])
                self.assertEqual(json.loads(path.read_text())["password"], options["password"])
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                exporter.terminate.assert_called_once()
                if enabled:
                    self.assertEqual(popen.call_args_list[1].args[0][0], "alloy")
                    alloy.terminate.assert_called_once()

    def test_child_exit_stops_its_peer_and_fails_the_app(self):
        with tempfile.TemporaryDirectory() as temp, contextlib.redirect_stdout(io.StringIO()):
            exporter, alloy = MagicMock(), MagicMock()
            exporter.poll.return_value = 1
            alloy.poll.return_value = None
            code, _ = self.invoke(Path(temp), self.options(), [exporter, alloy], stop=False)
            self.assertEqual(code, 1)
            alloy.terminate.assert_called_once()

    def test_second_process_start_failure_cleans_up_without_logging_secrets(self):
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as temp, contextlib.redirect_stdout(output):
            exporter = MagicMock()
            exporter.poll.return_value = None
            code, _ = self.invoke(Path(temp), self.options(), [exporter, OSError("metrics-secret")])
            self.assertEqual(code, 1)
            exporter.terminate.assert_called_once()
        self.assertNotIn("metrics-secret", output.getvalue())

    def test_malformed_json_never_launches_processes_or_logs_input(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            (directory / "options.json").write_text('{"password":"private-secret"')
            output = io.StringIO()
            with contextlib.redirect_stdout(output), patch("run.os.umask"), \
                 patch("run.subprocess.Popen") as popen:
                self.assertEqual(run.main(directory), 1)
                popen.assert_not_called()
            self.assertNotIn("private-secret", output.getvalue())


class PackagingTests(unittest.TestCase):
    def test_self_contained_context_excludes_credentials_and_caches(self):
        with tempfile.TemporaryDirectory() as temp:
            subprocess.run(["sh", str(ROOT / "scripts/package-homeassistant.sh"), temp],
                           check=True, capture_output=True, text=True)
            with tarfile.open(Path(temp) / "exporter-source.tar") as archive:
                names = {name.removeprefix("./") for name in archive.getnames()}
            self.assertIn("main.go", names)
            self.assertIn("sense.go", names)
            self.assertIn("go.mod", names)
            self.assertTrue(all(name == "go.mod" or name.endswith(".go") for name in names))
            self.assertNotIn("config.json", names)
            self.assertNotIn("zaptec-history.json", names)
            for file in ("Dockerfile", "config.yaml", "run.py", "config.alloy", "DOCS.md", ".dockerignore"):
                self.assertTrue((Path(temp) / file).is_file())

    def test_packaging_rejects_source_app_as_destination(self):
        result = subprocess.run(["sh", str(ROOT / "scripts/package-homeassistant.sh"),
                                 str(ROOT / "homeassistant/zaptec_cloud")],
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
