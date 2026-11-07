package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

type fileConfig struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ChargerID       string `json:"charger_id"`
	Mode            string `json:"mode"`
	UserGroupID     string `json:"user_group_id"`
	ListenAddr      string `json:"listen_addr"`
	PollInterval    string `json:"poll_interval"`
	StaleAfter      string `json:"stale_after"`
	HistoryEnabled  bool   `json:"history_enabled"`
	HistoryFrom     string `json:"history_from"`
	HistoryInterval string `json:"history_interval"`
	HistoryFile     string `json:"history_file"`
	SenseEnabled    bool   `json:"sense_enabled"`
	SenseInterval   string `json:"sense_interval"`
	SenseTimezone   string `json:"sense_timezone"`
}

// Runtime configuration is validated once, before requests or goroutines start.
type config struct {
	SenseEnabled                                                 bool
	SenseInterval                                                time.Duration
	SenseLocation                                                *time.Location
	Username, Password, ChargerID, Mode, UserGroupID, ListenAddr string
	PollInterval, StaleAfter, HistoryInterval                    time.Duration
	HistoryEnabled                                               bool
	HistoryFrom                                                  time.Time
	HistoryFile                                                  string
}

func loadConfig(path string, required bool, getenv func(string) string) (config, error) {
	raw := fileConfig{
		Mode: "rest", ListenAddr: ":8080", PollInterval: "1m", StaleAfter: "10m",
		HistoryEnabled: true, HistoryFrom: "1970-01-01T00:00:00Z",
		HistoryInterval: "1m", HistoryFile: "zaptec-history.json",
		SenseInterval: "5m", SenseTimezone: "Europe/Stockholm",
	}
	f, err := os.Open(path)
	if err != nil && (required || !errors.Is(err, os.ErrNotExist)) {
		return config{}, fmt.Errorf("open config: %w", err)
	}
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return config{}, fmt.Errorf("stat config: %w", err)
		}
		if info.Size() > 1<<20 {
			return config{}, errors.New("config file exceeds 1 MiB")
		}
		decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
		decoder.DisallowUnknownFields()
		fileValues := &raw
		if err := decoder.Decode(&fileValues); err != nil {
			return config{}, fmt.Errorf("decode config: %w", err)
		}
		if fileValues == nil {
			return config{}, errors.New("config must contain a JSON object")
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			return config{}, errors.New("config must contain exactly one JSON object")
		}
	}
	for _, setting := range []struct {
		env   string
		value *string
	}{
		{"ZAPTEC_USERNAME", &raw.Username},
		{"ZAPTEC_PASSWORD", &raw.Password},
		{"ZAPTEC_CHARGER_ID", &raw.ChargerID},
		{"ZAPTEC_MODE", &raw.Mode},
		{"ZAPTEC_USERGROUP_ID", &raw.UserGroupID},
		{"LISTEN_ADDR", &raw.ListenAddr},
		{"ZAPTEC_POLL_INTERVAL", &raw.PollInterval},
		{"ZAPTEC_STALE_AFTER", &raw.StaleAfter},
		{"ZAPTEC_HISTORY_FROM", &raw.HistoryFrom},
		{"ZAPTEC_HISTORY_INTERVAL", &raw.HistoryInterval},
		{"ZAPTEC_HISTORY_FILE", &raw.HistoryFile},
		{"ZAPTEC_SENSE_INTERVAL", &raw.SenseInterval},
		{"ZAPTEC_SENSE_TIMEZONE", &raw.SenseTimezone},
	} {
		if value := getenv(setting.env); value != "" {
			*setting.value = value
		}
	}
	for _, setting := range []struct {
		name   string
		target *bool
	}{
		{"ZAPTEC_HISTORY_ENABLED", &raw.HistoryEnabled},
		{"ZAPTEC_SENSE_ENABLED", &raw.SenseEnabled},
	} {
		if value := getenv(setting.name); value != "" {
			*setting.target, err = strconv.ParseBool(value)
			if err != nil {
				return config{}, fmt.Errorf("%s must be true or false", setting.name)
			}
		}
	}
	if raw.Username == "" || raw.Password == "" || raw.ChargerID == "" {
		return config{}, errors.New("username, password and charger_id are required in the config file or ZAPTEC_* environment variables")
	}
	if raw.Mode != "rest" && raw.Mode != "servicebus" {
		return config{}, errors.New("mode must be rest or servicebus")
	}
	if raw.Mode == "servicebus" && raw.UserGroupID == "" {
		return config{}, errors.New("user_group_id is required in servicebus mode")
	}
	if raw.ListenAddr == "" {
		return config{}, errors.New("listen_addr must not be empty")
	}
	cfg := config{
		Username: raw.Username, Password: raw.Password, ChargerID: raw.ChargerID,
		Mode: raw.Mode, UserGroupID: raw.UserGroupID, ListenAddr: raw.ListenAddr,
		HistoryEnabled: raw.HistoryEnabled, HistoryFile: raw.HistoryFile,
		SenseEnabled: raw.SenseEnabled,
	}
	for _, setting := range []struct {
		name, value string
		minimum     time.Duration
		target      *time.Duration
	}{
		{"poll_interval", raw.PollInterval, 30 * time.Second, &cfg.PollInterval},
		{"stale_after", raw.StaleAfter, time.Nanosecond, &cfg.StaleAfter},
	} {
		*setting.target, err = time.ParseDuration(setting.value)
		if err != nil || *setting.target < setting.minimum {
			return config{}, fmt.Errorf("%s must be a duration >= %s", setting.name, setting.minimum)
		}
	}
	if cfg.HistoryEnabled {
		cfg.HistoryInterval, err = time.ParseDuration(raw.HistoryInterval)
		if err != nil || cfg.HistoryInterval < time.Minute {
			return config{}, errors.New("history_interval must be a duration >= 1m")
		}
		cfg.HistoryFrom, err = time.Parse(time.RFC3339, raw.HistoryFrom)
		if err != nil || !cfg.HistoryFrom.Before(time.Now()) {
			return config{}, errors.New("history_from must be an RFC3339 timestamp in the past")
		}
		if cfg.HistoryFile == "" {
			return config{}, errors.New("history_file must not be empty")
		}
	}
	if cfg.SenseEnabled {
		cfg.SenseInterval, err = time.ParseDuration(raw.SenseInterval)
		if err != nil || cfg.SenseInterval < 5*time.Minute {
			return config{}, errors.New("sense_interval must be a duration >= 5m")
		}
		if raw.SenseTimezone == "" {
			return config{}, errors.New("sense_timezone must name an IANA timezone")
		}
		cfg.SenseLocation, err = time.LoadLocation(raw.SenseTimezone)
		if err != nil {
			return config{}, errors.New("sense_timezone must name an available IANA timezone")
		}
	}
	return cfg, nil
}
