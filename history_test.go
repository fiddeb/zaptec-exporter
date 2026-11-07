package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var historyFrom = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
var historyNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func sessionFixture(id string, energy float64, end time.Time) archivedSession {
	return archivedSession{ID: id, ChargerID: "charger", StartDateTime: end.Add(-time.Hour), EndDateTime: &end, RecognizedDateTime: end, Energy: energy}
}

func historyBody(sessions []archivedSession, cursor string, more bool) string {
	data, err := json.Marshal(map[string]any{"sessions": sessions, "cursor": cursor, "hasMore": more})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func historyMetrics(h *historyStore) string {
	r := httptest.NewRecorder()
	h.metrics(r, time.Hour, historyNow)
	return r.Body.String()
}

func requireMetric(t *testing.T, body, name, value string) {
	t.Helper()
	want := fmt.Sprintf("%s{charger_id=\"charger\"} %s\n", name, value)
	if !strings.Contains(body, want) {
		t.Fatalf("missing %q in:\n%s", want, body)
	}
}

func TestHistoryReconciliationPersistsCorrectionsAndLateSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h, err := openHistory(path, "charger", historyFrom)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(historyMetrics(h), "zaptec_charge_completed_energy_kwh") {
		t.Fatal("exported totals before a successful synchronization")
	}
	a := sessionFixture("a", 5, historyNow.Add(-24*time.Hour))
	b := sessionFixture("b", 10, historyNow.Add(-time.Hour))
	pageCalls := 0
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.URL.Path != "/api/sessions/archived" || q.Get("ChargerId") != "charger" || q.Get("From") != historyFrom.Format(time.RFC3339) || q.Get("To") != historyNow.Format(time.RFC3339) || q.Get("PageSize") != "50" || q.Get("Order") != "0" {
			t.Fatalf("unexpected archive request: %s", r.URL)
		}
		pageCalls++
		if q.Get("Cursor") == "" {
			return testResponse(200, historyBody([]archivedSession{a, b}, "opaque+/=cursor", true)), nil
		}
		if q.Get("Cursor") != "opaque+/=cursor" {
			t.Fatal("cursor was not preserved")
		}
		return testResponse(200, historyBody([]archivedSession{b}, "", false)), nil
	})
	if err := h.refresh(context.Background(), client, historyNow); err != nil || pageCalls != 2 {
		t.Fatalf("refresh: %v, pages=%d", err, pageCalls)
	}
	requireMetric(t, historyMetrics(h), "zaptec_charge_completed_energy_kwh", "15")
	requireMetric(t, historyMetrics(h), "zaptec_charge_completed_sessions", "2")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("history must be private: %v %v", info, err)
	}

	// Restart, then discover a correction and a delayed offline session whose end
	// time is much older than the last synchronization.
	h, err = openHistory(path, "CHARGER", historyFrom)
	if err != nil {
		t.Fatal(err)
	}
	requireMetric(t, historyMetrics(h), "zaptec_charge_completed_energy_kwh", "15")
	a.Voided, a.ReplacedBySessionID = true, "d"
	a.RecognizedDateTime = historyNow
	c := sessionFixture("c", 3, historyNow.Add(-100*24*time.Hour))
	c.RecognizedDateTime = historyNow
	d := sessionFixture("d", 6, historyNow.Add(-24*time.Hour))
	client = testAPI(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("From") != historyFrom.Format(time.RFC3339) {
			t.Fatal("moving lookback would miss the delayed session")
		}
		return testResponse(200, historyBody([]archivedSession{a, b, c, d}, "", false)), nil
	})
	if err := h.refresh(context.Background(), client, historyNow); err != nil {
		t.Fatal(err)
	}
	body := historyMetrics(h)
	requireMetric(t, body, "zaptec_charge_completed_energy_kwh", "19")
	requireMetric(t, body, "zaptec_charge_completed_sessions", "3")
	requireMetric(t, body, "zaptec_charge_last_session_energy_kwh", "10")
	requireMetric(t, body, "zaptec_charge_last_session_end_timestamp_seconds", fmt.Sprint(b.EndDateTime.Unix()))
	requireMetric(t, body, "zaptec_charge_last_session_duration_seconds", "3600")
	if !strings.Contains(body, "zaptec_exporter_history_up 1\n") {
		t.Fatal("successful synchronization reported down")
	}

	// A partial fetch must not overwrite either the in-memory or persisted totals.
	before, _ := os.ReadFile(path)
	client = testAPI(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("Cursor") == "" {
			return testResponse(200, historyBody([]archivedSession{b}, "next", true)), nil
		}
		return testResponse(500, `{}`), nil
	})
	if err := h.refresh(context.Background(), client, historyNow.Add(time.Hour)); err == nil {
		t.Fatal("accepted partial archive")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("partial fetch modified history file")
	}
	requireMetric(t, historyMetrics(h), "zaptec_charge_completed_energy_kwh", "19")
	if !strings.Contains(historyMetrics(h), "zaptec_exporter_history_up 0\n") {
		t.Fatal("failed synchronization reported up")
	}
}

func TestArchiveRejectsInvalidResponses(t *testing.T) {
	valid := sessionFixture("a", 5, historyNow.Add(-time.Hour))
	foreign := valid
	foreign.ChargerID = "other-charger"
	negative := valid
	negative.Energy = -1
	for _, tt := range []struct{ name, body string }{
		{"missing sessions", `{}`},
		{"missing cursor", historyBody([]archivedSession{valid}, "", true)},
		{"repeated cursor", historyBody([]archivedSession{valid}, "same", true)},
		{"foreign charger", historyBody([]archivedSession{foreign}, "", false)},
		{"negative energy", historyBody([]archivedSession{negative}, "", false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := testAPI(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 2 {
					t.Fatal("unbounded pagination")
				}
				return testResponse(200, tt.body), nil
			})
			if _, err := client.archivedSessions(context.Background(), "charger", historyFrom, historyNow); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestHistoryFileValidationAndWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h, err := openHistory(path, "charger", historyFrom)
	if err != nil {
		t.Fatal(err)
	}
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		return testResponse(200, historyBody([]archivedSession{sessionFixture("a", 5, historyNow.Add(-time.Hour))}, "", false)), nil
	})
	if err := h.refresh(context.Background(), client, historyNow); err != nil {
		t.Fatal(err)
	}
	if _, err := openHistory(path, "other-charger", historyFrom); err == nil {
		t.Fatal("accepted another charger's cache")
	}
	if _, err := openHistory(path, "charger", historyFrom.Add(time.Hour)); err == nil {
		t.Fatal("accepted another time range")
	}
	// A nonexistent parent prevents a durable write after a successful API fetch.
	h.file = filepath.Join(t.TempDir(), "missing", "history.json")
	client = testAPI(func(r *http.Request) (*http.Response, error) {
		return testResponse(200, historyBody(nil, "", false)), nil
	})
	if err := h.refresh(context.Background(), client, historyNow); err == nil {
		t.Fatal("reported success without persisting data")
	}
	requireMetric(t, historyMetrics(h), "zaptec_charge_completed_energy_kwh", "5")
	if err := os.WriteFile(path, []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openHistory(path, "charger", historyFrom); err == nil {
		t.Fatal("silently discarded corrupt history")
	}
}

func TestHistoryWithoutKnownEndDoesNotInventLastSession(t *testing.T) {
	session := sessionFixture("a", 5, historyNow)
	session.EndDateTime = nil
	h := &historyStore{snapshot: historySnapshot{ChargerID: "charger", From: historyFrom, Sessions: map[string]archivedSession{"a": session}}}
	body := historyMetrics(h)
	requireMetric(t, body, "zaptec_charge_completed_energy_kwh", "5")
	if strings.Contains(body, "zaptec_charge_last_session_") {
		t.Fatal("invented a session end time")
	}
}

func TestHistoryExcludesVoidedAndReplacedSessionsIndependently(t *testing.T) {
	valid := sessionFixture("valid", 4, historyNow)
	voided := sessionFixture("voided", 10, historyNow)
	voided.Voided = true
	replaced := sessionFixture("replaced", 20, historyNow)
	replaced.ReplacedBySessionID = "replacement-outside-range"
	h := &historyStore{snapshot: historySnapshot{ChargerID: "charger", From: historyFrom, Sessions: map[string]archivedSession{
		valid.ID: valid, voided.ID: voided, replaced.ID: replaced,
	}}}
	body := historyMetrics(h)
	requireMetric(t, body, "zaptec_charge_completed_energy_kwh", "4")
	requireMetric(t, body, "zaptec_charge_completed_sessions", "1")
}

func TestArchiveDeduplicatesUsingLatestRevision(t *testing.T) {
	newer := sessionFixture("SESSION", 7, historyNow.Add(-time.Hour))
	older := newer
	older.ID = "session"
	older.Energy = 100
	older.RecognizedDateTime = newer.RecognizedDateTime.Add(-time.Hour)
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		return testResponse(200, historyBody([]archivedSession{newer, older}, "", false)), nil
	})
	items, err := client.archivedSessions(context.Background(), "charger", historyFrom, historyNow)
	if err != nil || len(items) != 1 || items["session"].Energy != 7 {
		t.Fatalf("incorrect deduplication: %v, %+v", err, items)
	}
}
