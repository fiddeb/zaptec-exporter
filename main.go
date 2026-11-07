package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://api.zaptec.com"

type observation struct {
	ChargerID string    `json:"chargerId"`
	StateID   int       `json:"stateId"`
	StateName string    `json:"stateName"`
	Timestamp time.Time `json:"timestamp"`
	Value     string    `json:"valueAsString"`
}

func (o *observation) UnmarshalJSON(data []byte) error {
	type fields observation
	var decoded observation
	wire := struct {
		*fields
		Timestamp *string `json:"timestamp"`
	}{fields: (*fields)(&decoded)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Timestamp != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *wire.Timestamp)
		if err != nil {
			// Zaptec REST timestamps are UTC, but may omit the trailing Z.
			parsed, err = time.Parse("2006-01-02T15:04:05.999999999", *wire.Timestamp)
		}
		if err != nil {
			return fmt.Errorf("invalid observation timestamp: %w", err)
		}
		decoded.Timestamp = parsed
	}
	*o = decoded
	return nil
}

type stateStore struct {
	mu        sync.RWMutex
	data      map[int]observation
	lastFetch time.Time
	fetchOK   bool
}

func newStore() *stateStore { return &stateStore{data: make(map[int]observation)} }
func (s *stateStore) apply(items []observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		if old, ok := s.data[item.StateID]; !ok || !item.Timestamp.Before(old.Timestamp) {
			s.data[item.StateID] = item
		}
	}
}
func (s *stateStore) recordFetch(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetchOK = ok
	if ok {
		s.lastFetch = time.Now()
	}
}

type apiClient struct {
	http               *http.Client
	username, password string
	mu                 sync.Mutex
	token              string
	expires            time.Time
}

func (c *apiClient) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > 60*time.Second {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"password"}, "username": {c.username}, "password": {c.password}, "scope": {"openid"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "zaptec-exporter/dev")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", &apiHTTPError{StatusCode: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", errors.New("token response did not include access_token")
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 300
	}
	c.token = payload.AccessToken
	c.expires = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return c.token, nil
}
func (c *apiClient) get(ctx context.Context, path string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		err := c.getOnce(ctx, path, out)
		if !errors.Is(err, errUnauthorized) || attempt == 1 {
			return err
		}
	}
	return errUnauthorized
}

var errUnauthorized = errors.New("Zaptec API rejected access token")

func (c *apiClient) getOnce(ctx context.Context, path string, out any) error {
	token, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "zaptec-exporter/dev")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		// A concurrent request may already have refreshed this token.
		if c.token == token {
			c.token = ""
			c.expires = time.Time{}
		}
		c.mu.Unlock()
		return errUnauthorized
	}
	if resp.StatusCode != 200 {
		return &apiHTTPError{StatusCode: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
func (c *apiClient) snapshot(ctx context.Context, id string) ([]observation, error) {
	var obs []observation
	err := c.get(ctx, "/api/chargers/"+url.PathEscape(id)+"/state", &obs)
	return obs, err
}
func (s *stateStore) metrics(w http.ResponseWriter, chargerID string, staleAfter time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	now := time.Now()
	up := 0
	if s.fetchOK && !s.lastFetch.IsZero() && now.Sub(s.lastFetch) <= staleAfter {
		up = 1
	}
	fmt.Fprintf(w, "# HELP zaptec_exporter_up Whether recent Zaptec REST retrieval succeeded.\n# TYPE zaptec_exporter_up gauge\nzaptec_exporter_up %d\n", up)
	lastSuccess := float64(0)
	if !s.lastFetch.IsZero() {
		lastSuccess = float64(s.lastFetch.Unix())
	}
	fmt.Fprintf(w, "# HELP zaptec_exporter_last_success_timestamp_seconds Last successful REST snapshot.\n# TYPE zaptec_exporter_last_success_timestamp_seconds gauge\nzaptec_exporter_last_success_timestamp_seconds %.0f\n", lastSuccess)
	definitions := []struct {
		id         int
		name, help string
	}{
		{513, "zaptec_charge_power_watts", "Instant charge power in watts"},
		{553, "zaptec_charge_session_energy_kwh", "Energy in the active charging session in kWh"},
		{708, "zaptec_charge_current_limit_amperes", "Allocated charging current in amperes"},
		{710, "zaptec_charger_operation_mode", "Charger operation mode enum"},
		{-2, "zaptec_charger_online", "Charger online status"},
		{519, "zaptec_charge_phases_mode", "Configured charging phases enum"},
	}
	for _, d := range definitions {
		metricHeader(w, d.name, d.help)
		exportValue(w, s.data, d.id, d.name, chargerID, "", staleAfter, now)
	}
	for _, d := range []struct {
		baseID     int
		name, help string
	}{
		{506, "zaptec_charge_current_amperes", "Charging current in amperes by phase"},
		{500, "zaptec_charge_voltage_volts", "Charger voltage in volts by phase"},
	} {
		metricHeader(w, d.name, d.help)
		for phase := 1; phase <= 3; phase++ {
			exportValue(w, s.data, d.baseID+phase, d.name, chargerID, strconv.Itoa(phase), staleAfter, now)
		}
	}
}
func metricHeader(w io.Writer, name, help string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
}
func exportValue(w io.Writer, items map[int]observation, id int, name, charger, phase string, staleAfter time.Duration, now time.Time) {
	item, ok := items[id]
	if !ok || item.Timestamp.IsZero() || now.Sub(item.Timestamp) > staleAfter {
		return
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(item.Value), 64)
	if err != nil {
		return
	}
	label := fmt.Sprintf("charger_id=%q", charger)
	if phase != "" {
		label += fmt.Sprintf(",phase=%q", phase)
	}
	fmt.Fprintf(w, "%s{%s} %g\n", name, label, value)
}
func main() {
	configPath := flag.String("config", "config.json", "JSON configuration file (default file is optional when using environment variables)")
	flag.Parse()
	explicitConfig := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicitConfig = true
		}
	})
	cfg, err := loadConfig(*configPath, explicitConfig, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	client := &apiClient{http: &http.Client{Timeout: 15 * time.Second}, username: cfg.Username, password: cfg.Password}
	ctx := context.Background()
	resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	chargerID, err := client.resolveChargerID(resolveCtx, cfg.ChargerID)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	if chargerID != cfg.ChargerID {
		log.Printf("resolved charger_id to %s", chargerID)
	}
	cfg.ChargerID = chargerID
	var history *historyStore
	if cfg.HistoryEnabled {
		history, err = openHistory(cfg.HistoryFile, cfg.ChargerID, cfg.HistoryFrom)
		if err != nil {
			log.Fatal(err)
		}
	}
	store := newStore()
	// Both modes begin with a REST snapshot, which also handles reconnection gaps.
	refresh := func() {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		obs, err := client.snapshot(requestCtx, cfg.ChargerID)
		if err != nil {
			log.Printf("snapshot error: %v", err)
			store.recordFetch(false)
			return
		}
		store.apply(obs)
		store.recordFetch(true)
		log.Printf("snapshot: %d observations", len(obs))
	}
	refresh()
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for range ticker.C {
			refresh()
		}
	}()
	if history != nil {
		go func() {
			for {
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				err := history.refresh(requestCtx, client, time.Now().UTC())
				cancel()
				if err != nil {
					log.Printf("history synchronization error: %v", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(cfg.HistoryInterval):
				}
			}
		}()
	}
	var sense *senseStore
	if cfg.SenseEnabled {
		sense = &senseStore{}
		go sense.poll(ctx, client, cfg.ChargerID, cfg.SenseInterval, cfg.SenseLocation)
	}
	if cfg.Mode == "servicebus" {
		go func() {
			if err := runServiceBus(ctx, client, cfg.UserGroupID, cfg.ChargerID, store); err != nil {
				log.Printf("servicebus stopped: %v", err)
			}
		}()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		store.metrics(w, cfg.ChargerID, cfg.StaleAfter)
		if history != nil {
			history.metrics(w, cfg.HistoryInterval, time.Now())
		}
		if sense != nil {
			sense.metrics(w, cfg.SenseInterval, cfg.SenseLocation, time.Now())
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); fmt.Fprintln(w, "ok") })
	log.Printf("listening on %s (mode=%s)", cfg.ListenAddr, cfg.Mode)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
