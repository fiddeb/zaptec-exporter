package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Keep only accounting fields, never user identities, tokens or signatures.
type archivedSession struct {
	ID                  string     `json:"id"`
	ChargerID           string     `json:"chargerId"`
	StartDateTime       time.Time  `json:"startDateTime"`
	EndDateTime         *time.Time `json:"endDateTime"`
	RecognizedDateTime  time.Time  `json:"recognizedDateTime"`
	Energy              float64    `json:"energy"`
	Voided              bool       `json:"voided"`
	ReplacedBySessionID string     `json:"replacedBySessionId,omitempty"`
}

func (s archivedSession) validate(chargerID string) error {
	if s.ID == "" || s.StartDateTime.IsZero() || !strings.EqualFold(s.ChargerID, chargerID) {
		return errors.New("invalid archived session identity or start time")
	}
	if s.Energy < 0 || math.IsNaN(s.Energy) || math.IsInf(s.Energy, 0) {
		return errors.New("invalid archived session energy")
	}
	return nil
}

func (c *apiClient) archivedSessions(ctx context.Context, chargerID string, from, to time.Time) (map[string]archivedSession, error) {
	if !from.Before(to) {
		return nil, errors.New("archive start must precede end")
	}
	query := url.Values{
		"ChargerId": {chargerID},
		"From":      {from.UTC().Format(time.RFC3339Nano)},
		"To":        {to.UTC().Format(time.RFC3339Nano)},
		"PageSize":  {"50"},
		"Order":     {"0"},
	}
	sessions := make(map[string]archivedSession)
	seenCursors := make(map[string]bool)
	// Bound work even if a faulty upstream endlessly supplies new cursors.
	for page := 0; page < 1000; page++ {
		var response struct {
			Sessions json.RawMessage `json:"sessions"`
			Cursor   string          `json:"cursor"`
			HasMore  bool            `json:"hasMore"`
		}
		if err := c.get(ctx, "/api/sessions/archived?"+query.Encode(), &response); err != nil {
			return nil, err
		}
		if len(response.Sessions) == 0 {
			return nil, errors.New("archive response missing sessions")
		}
		var items []archivedSession
		if err := json.Unmarshal(response.Sessions, &items); err != nil {
			return nil, err
		}
		for _, session := range items {
			if err := session.validate(chargerID); err != nil {
				return nil, err
			}
			key := strings.ToLower(session.ID)
			old, exists := sessions[key]
			// Repeated IDs across pages must not double count. Prefer the latest revision.
			if !exists || !session.RecognizedDateTime.Before(old.RecognizedDateTime) {
				sessions[key] = session
			}
		}
		if !response.HasMore {
			return sessions, nil
		}
		if response.Cursor == "" || seenCursors[response.Cursor] {
			return nil, errors.New("archive returned missing or repeated page cursor")
		}
		seenCursors[response.Cursor] = true
		query.Set("Cursor", response.Cursor)
	}
	return nil, errors.New("archive exceeded 1000 pages; narrow ZAPTEC_HISTORY_FROM")
}

type historySnapshot struct {
	Version     int                        `json:"version"`
	ChargerID   string                     `json:"chargerId"`
	From        time.Time                  `json:"from"`
	LastSuccess time.Time                  `json:"lastSuccess"`
	Sessions    map[string]archivedSession `json:"sessions"`
}

type historyStore struct {
	mu       sync.RWMutex
	file     string
	snapshot historySnapshot
	fetchOK  bool
}

func openHistory(file, chargerID string, from time.Time) (*historyStore, error) {
	h := &historyStore{file: file, snapshot: historySnapshot{
		Version: 1, ChargerID: chargerID, From: from,
	}}
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 64<<20 {
		return nil, errors.New("history file exceeds 64 MiB")
	}
	var saved historySnapshot
	decoder := json.NewDecoder(io.LimitReader(f, 64<<20))
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("history file has trailing or invalid JSON")
	}
	if saved.Version != 1 || !strings.EqualFold(saved.ChargerID, chargerID) || !saved.From.Equal(from) {
		return nil, errors.New("history file belongs to a different charger, time range or version; choose another ZAPTEC_HISTORY_FILE")
	}
	if saved.LastSuccess.IsZero() || saved.Sessions == nil {
		return nil, errors.New("history file lacks a completed snapshot")
	}
	for id, session := range saved.Sessions {
		if err := session.validate(chargerID); err != nil {
			return nil, err
		}
		if id != strings.ToLower(session.ID) {
			return nil, errors.New("history session key does not match its ID")
		}
	}
	h.snapshot = saved
	return h, nil
}

// Re-read the fixed range: end-time filters alone cannot discover late arrivals
// or corrections to sessions outside a moving lookback window.
func (h *historyStore) refresh(ctx context.Context, client *apiClient, now time.Time) error {
	h.mu.RLock()
	next := h.snapshot
	h.mu.RUnlock()
	sessions, err := client.archivedSessions(ctx, next.ChargerID, next.From, now)
	if err == nil {
		next.Sessions = sessions
		next.LastSuccess = now
		err = saveHistory(h.file, next)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fetchOK = err == nil
	if err == nil {
		h.snapshot = next
	}
	return err
}

func saveHistory(path string, snapshot historySnapshot) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".zaptec-history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(snapshot); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (h *historyStore) metrics(w io.Writer, interval time.Duration, now time.Time) {
	h.mu.RLock()
	// Published snapshots are immutable; never hold the mutex while serving a client.
	snapshot := h.snapshot
	fetchOK := h.fetchOK
	h.mu.RUnlock()
	up := 0
	if fetchOK && now.Sub(snapshot.LastSuccess) <= 2*interval {
		up = 1
	}
	metricHeader(w, "zaptec_exporter_history_up", "Whether the latest archive synchronization succeeded and is recent")
	fmt.Fprintf(w, "zaptec_exporter_history_up %d\n", up)
	metricHeader(w, "zaptec_exporter_history_last_success_timestamp_seconds", "Last fully persisted archive synchronization")
	lastSuccess := int64(0)
	if !snapshot.LastSuccess.IsZero() {
		lastSuccess = snapshot.LastSuccess.Unix()
	}
	fmt.Fprintf(w, "zaptec_exporter_history_last_success_timestamp_seconds %d\n", lastSuccess)
	if snapshot.Sessions == nil {
		return // No successful synchronization yet: do not invent zero totals.
	}
	var energy float64
	var count int
	var last *archivedSession
	for _, session := range snapshot.Sessions {
		if session.Voided || session.ReplacedBySessionID != "" {
			continue
		}
		energy += session.Energy
		count++
		if session.EndDateTime != nil && (last == nil || session.EndDateTime.After(*last.EndDateTime) || (session.EndDateTime.Equal(*last.EndDateTime) && session.ID > last.ID)) {
			copy := session
			last = &copy
		}
	}
	label := fmt.Sprintf("charger_id=%q", snapshot.ChargerID)
	metricHeader(w, "zaptec_charge_completed_energy_kwh", "Energy of non-void archived sessions since the configured history start; corrections can decrease it")
	fmt.Fprintf(w, "zaptec_charge_completed_energy_kwh{%s} %g\n", label, energy)
	metricHeader(w, "zaptec_charge_completed_sessions", "Number of non-void archived sessions since the configured history start")
	fmt.Fprintf(w, "zaptec_charge_completed_sessions{%s} %d\n", label, count)
	metricHeader(w, "zaptec_charge_history_from_timestamp_seconds", "Inclusive configured lower bound for archived session end times")
	fmt.Fprintf(w, "zaptec_charge_history_from_timestamp_seconds{%s} %d\n", label, snapshot.From.Unix())
	if last != nil {
		metricHeader(w, "zaptec_charge_last_session_energy_kwh", "Energy of the latest completed session with a known end time")
		fmt.Fprintf(w, "zaptec_charge_last_session_energy_kwh{%s} %g\n", label, last.Energy)
		metricHeader(w, "zaptec_charge_last_session_end_timestamp_seconds", "Actual end time of the latest completed session")
		fmt.Fprintf(w, "zaptec_charge_last_session_end_timestamp_seconds{%s} %d\n", label, last.EndDateTime.Unix())
		if !last.EndDateTime.Before(last.StartDateTime) {
			metricHeader(w, "zaptec_charge_last_session_duration_seconds", "Elapsed connection time of the latest completed session, including pauses")
			fmt.Fprintf(w, "zaptec_charge_last_session_duration_seconds{%s} %g\n", label, last.EndDateTime.Sub(last.StartDateTime).Seconds())
		}
	}
}
