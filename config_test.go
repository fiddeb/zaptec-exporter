package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) string { return "" }

func TestConfigFileOnly(t *testing.T) {
	path := writeConfig(t, `{
		"username":"file-user", "password":"file-password", "charger_id":"charger",
		"mode":"servicebus", "user_group_id":"group", "listen_addr":"127.0.0.1:8081",
		"poll_interval":"90s", "stale_after":"10m", "history_enabled":true,
		"history_from":"2025-01-01T00:00:00Z", "history_interval":"2h", "history_file":"/data/history.json"
	}`)
	cfg, err := loadConfig(path, true, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "file-user" || cfg.Password != "file-password" || cfg.ChargerID != "charger" || cfg.UserGroupID != "group" || cfg.Mode != "servicebus" || cfg.ListenAddr != "127.0.0.1:8081" {
		t.Fatal("file connection settings were not loaded")
	}
	if cfg.PollInterval != 90*time.Second || cfg.StaleAfter != 10*time.Minute || cfg.HistoryInterval != 2*time.Hour || !cfg.HistoryEnabled || cfg.HistoryFile != "/data/history.json" || cfg.HistoryFrom.Format(time.RFC3339) != "2025-01-01T00:00:00Z" {
		t.Fatal("file timing and history settings were not loaded")
	}
}

func TestMinimalConfigDefaultsAndExplicitFalse(t *testing.T) {
	for _, history := range []bool{true, false} {
		body := `{"username":"user","password":"password","charger_id":"charger"}`
		if !history {
			body = strings.TrimSuffix(body, "}") + `,"history_enabled":false}`
		}
		cfg, err := loadConfig(writeConfig(t, body), true, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mode != "rest" || cfg.PollInterval != time.Minute || cfg.StaleAfter != 10*time.Minute || cfg.ListenAddr != ":8080" || cfg.HistoryEnabled != history {
			t.Fatal("incorrect defaults or ignored explicit false")
		}
	}
}

func TestConfigEnvironmentOverridesAndLegacyMode(t *testing.T) {
	values := map[string]string{
		"ZAPTEC_USERNAME": "env-user", "ZAPTEC_PASSWORD": "env-password", "ZAPTEC_CHARGER_ID": "env-charger",
		"ZAPTEC_MODE": "servicebus", "ZAPTEC_USERGROUP_ID": "env-group", "LISTEN_ADDR": "127.0.0.1:9000",
		"ZAPTEC_POLL_INTERVAL": "2m", "ZAPTEC_STALE_AFTER": "20m", "ZAPTEC_HISTORY_ENABLED": "true",
		"ZAPTEC_HISTORY_FROM": "2024-01-01T00:00:00Z", "ZAPTEC_HISTORY_INTERVAL": "3h", "ZAPTEC_HISTORY_FILE": "env-history.json",
	}
	getenv := func(key string) string { return values[key] }
	path := writeConfig(t, `{"username":"file-user","password":"file-password","charger_id":"file-charger","history_enabled":false}`)
	for _, input := range []string{path, filepath.Join(t.TempDir(), "missing-default.json")} {
		cfg, err := loadConfig(input, false, getenv)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Username != "env-user" || cfg.Password != "env-password" || cfg.ChargerID != "env-charger" || cfg.Mode != "servicebus" || cfg.UserGroupID != "env-group" || cfg.ListenAddr != "127.0.0.1:9000" || cfg.PollInterval != 2*time.Minute || cfg.StaleAfter != 20*time.Minute || !cfg.HistoryEnabled || cfg.HistoryInterval != 3*time.Hour || cfg.HistoryFile != "env-history.json" || cfg.HistoryFrom.Year() != 2024 {
			t.Fatal("environment did not override configuration")
		}
	}
	if _, err := loadConfig(filepath.Join(t.TempDir(), "explicit-missing.json"), true, getenv); err == nil {
		t.Fatal("silently ignored an explicitly requested missing config")
	}
}

func TestConfigRejectsInvalidInputWithoutLeakingPassword(t *testing.T) {
	for _, suffix := range []string{
		`,"mode":"invalid"}`, `,"mode":"servicebus"}`, `,"poll_interval":"29s"}`,
		`,"poll_interval":"secret-value"}`, `,"stale_after":"0s"}`, `,"history_interval":"30s"}`,
		`,"history_from":"not-a-timestamp"}`, `,"history_from":"2999-01-01T00:00:00Z"}`,
		`,"history_file":""}`, `,"listen_addr":""}`, `,"history_enabeld":false}`,
		`,"history_enabled":"false"}`, `} {}`, ``,
	} {
		path := writeConfig(t, `{"username":"user","password":"secret-value","charger_id":"charger"`+suffix)
		_, err := loadConfig(path, true, noEnv)
		if err == nil {
			t.Fatalf("accepted invalid configuration ending in %q", suffix)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatal("configuration error leaked a secret")
		}
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"username":"user","charger_id":"charger"}`} {
		if _, err := loadConfig(writeConfig(t, body), true, noEnv); err == nil {
			t.Fatal("accepted incomplete or non-object config")
		}
	}
}

func TestSenseConfigDefaultsOverridesAndValidation(t *testing.T) {
	base := `{"username":"user","password":"password","charger_id":"charger"`
	cfg, err := loadConfig(writeConfig(t, base+`}`), true, noEnv)
	if err != nil || cfg.SenseEnabled || cfg.PollInterval != time.Minute || cfg.HistoryInterval != time.Minute {
		t.Fatalf("unexpected defaults: %v", err)
	}
	cfg, err = loadConfig(writeConfig(t, base+`,"sense_enabled":true}`), true, noEnv)
	if err != nil || !cfg.SenseEnabled || cfg.SenseInterval != 5*time.Minute || cfg.SenseLocation.String() != "Europe/Stockholm" {
		t.Fatalf("unexpected Sense defaults: %v", err)
	}
	getenv := func(key string) string {
		return map[string]string{"ZAPTEC_SENSE_ENABLED": "true", "ZAPTEC_SENSE_INTERVAL": "10m", "ZAPTEC_SENSE_TIMEZONE": "UTC"}[key]
	}
	cfg, err = loadConfig(writeConfig(t, base+`,"sense_enabled":false,"sense_interval":"5m","sense_timezone":"Europe/Stockholm"}`), true, getenv)
	if err != nil || !cfg.SenseEnabled || cfg.SenseInterval != 10*time.Minute || cfg.SenseLocation.String() != "UTC" {
		t.Fatalf("Sense env overrides failed: %v", err)
	}
	for _, suffix := range []string{`,"sense_enabled":true,"sense_interval":"1m"}`, `,"sense_enabled":true,"sense_timezone":"invalid/timezone"}`, `,"sense_enabled":true,"sense_timezone":""}`} {
		if _, err := loadConfig(writeConfig(t, base+suffix), true, noEnv); err == nil {
			t.Fatalf("accepted invalid Sense settings: %s", suffix)
		}
	}
}
