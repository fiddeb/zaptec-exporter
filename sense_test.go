package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func senseNumber(value float64) *float64 { return &value }

func stockholm(t *testing.T) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	return zone
}

func energyPoints(from, to time.Time, value float64) []senseEnergyPoint {
	var points []senseEnergyPoint
	for slot := from; slot.Before(to); slot = slot.Add(time.Hour) {
		points = append(points, senseEnergyPoint{IntervalStart: slot, Interval: "01:00:00", Value: senseNumber(value)})
	}
	return points
}

func senseFixture(t *testing.T, now time.Time) string {
	t.Helper()
	from := senseDayStart(now, stockholm(t)).AddDate(0, 0, -1)
	data := senseData{
		LastReading: &senseReading{
			ObservedAt:    now.Add(-time.Minute),
			CurrentPhase1: senseNumber(1), CurrentPhase2: senseNumber(2), CurrentPhase3: senseNumber(3),
			VoltagePhase1: senseNumber(230), VoltagePhase2: senseNumber(231),
			ChargeCurrentPhase1: senseNumber(0), ChargeCurrentPhase2: senseNumber(0), ChargeCurrentPhase3: senseNumber(0),
			AvailableCurrentPhase1: senseNumber(6), AvailableCurrentPhase2: senseNumber(6), AvailableCurrentPhase3: senseNumber(6),
			CurrentNeutral: senseNumber(0),
		},
		TotalEnergy: energyPoints(from, now, 2), ChargeEnergy: energyPoints(from, now, 0.5),
		Readings:   []sensePowerPoint{{Timestamp: now.Add(-time.Minute), Power: senseNumber(2.5)}},
		PowerLimit: senseNumber(17.25),
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func senseMetrics(s *senseStore, zone *time.Location, now time.Time) string {
	r := httptest.NewRecorder()
	s.metrics(r, 5*time.Minute, zone, now)
	return r.Body.String()
}

func TestSensePhaseAndDailyMetricsAreCached(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 30, 0, 0, time.UTC)
	zone := stockholm(t)
	from := senseDayStart(now, zone).AddDate(0, 0, -1)
	requests := 0
	body := senseFixture(t, now)
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		requests++
		q := r.URL.Query()
		if r.URL.Path != "/api/installation/"+chargerUUID+"/energySensorData" || q.Get("from") != from.UTC().Format(time.RFC3339Nano) || q.Get("to") != now.Format(time.RFC3339Nano) || q.Get("resolution") != "day1" {
			t.Fatalf("unexpected Sense request: %s", r.URL)
		}
		return testResponse(200, body), nil
	})
	s := &senseStore{}
	if strings.Contains(senseMetrics(s, zone, now), "zaptec_sense_current_amperes") {
		t.Fatal("invented initial measurements")
	}
	if err := s.refresh(context.Background(), client, chargerUUID, zone, now); err != nil {
		t.Fatal(err)
	}
	metrics := senseMetrics(s, zone, now)
	for _, want := range []string{
		"zaptec_exporter_sense_up 1\n",
		fmt.Sprintf("zaptec_sense_current_amperes{installation_id=%q,phase=\"3\"} 3\n", chargerUUID),
		fmt.Sprintf("zaptec_sense_charge_current_amperes{installation_id=%q,phase=\"1\"} 0\n", chargerUUID),
		fmt.Sprintf("zaptec_sense_power_watts{installation_id=%q} 2500\n", chargerUUID),
		fmt.Sprintf("zaptec_sense_power_limit_watts{installation_id=%q} 17250\n", chargerUUID),
		fmt.Sprintf("zaptec_sense_total_energy_kwh{installation_id=%q,period=\"yesterday\"} 48\n", chargerUUID),
		fmt.Sprintf("zaptec_sense_charge_energy_kwh{installation_id=%q,period=\"yesterday\"} 12\n", chargerUUID),
		fmt.Sprintf("zaptec_sense_total_energy_kwh{installation_id=%q,period=\"today\"} 28\n", chargerUUID),
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing %q in:\n%s", want, metrics)
		}
	}
	if strings.Contains(metrics, fmt.Sprintf("zaptec_sense_voltage_volts{installation_id=%q,phase=\"3\"}", chargerUUID)) {
		t.Fatal("invented missing phase voltage")
	}
	if strings.Contains(metrics, "zaptec_sense_dynamic_power_limit_watts") {
		t.Fatal("invented missing dynamic limit")
	}
	for i := 0; i < 5; i++ {
		senseMetrics(s, zone, now)
	}
	if requests != 1 {
		t.Fatal("Prometheus scrapes triggered API requests")
	}
	for _, name := range []string{"zaptec_sense_current_amperes", "zaptec_sense_total_energy_kwh", "zaptec_sense_energy_complete"} {
		if strings.Count(metrics, "# TYPE "+name+" gauge") != 1 {
			t.Fatal("duplicate metric metadata")
		}
	}
	stale := senseMetrics(s, zone, now.Add(11*time.Minute))
	if !strings.Contains(stale, "zaptec_exporter_sense_up 0\n") || strings.Contains(stale, "zaptec_sense_current_amperes{") || strings.Contains(stale, "zaptec_sense_power_watts{") {
		t.Fatal("stale data reported as current")
	}

	// Failed fetches retain the last snapshot, with health down and its source time unchanged.
	client = testAPI(func(r *http.Request) (*http.Response, error) { return testResponse(500, `{}`), nil })
	if err := s.refresh(context.Background(), client, chargerUUID, zone, now.Add(time.Minute)); err == nil {
		t.Fatal("accepted server error")
	}
	failed := senseMetrics(s, zone, now.Add(time.Minute))
	if !strings.Contains(failed, "zaptec_exporter_sense_up 0\n") || !strings.Contains(failed, "zaptec_sense_current_amperes{") {
		t.Fatal("failed fetch discarded valid cached observations")
	}
}

func TestSenseEnergyDayBoundariesDSTAndMissingHours(t *testing.T) {
	zone := stockholm(t)
	for _, tt := range []struct {
		month      time.Month
		day, hours int
	}{{time.March, 29, 23}, {time.October, 25, 25}, {time.November, 6, 24}} {
		from := time.Date(2026, tt.month, tt.day, 0, 0, 0, 0, zone)
		to := from.AddDate(0, 0, 1)
		points := energyPoints(from, to, 1)
		// Boundaries from adjacent days must not contribute to this day.
		points = append(points, senseEnergyPoint{IntervalStart: from.Add(-time.Hour), Interval: "01:00:00", Value: senseNumber(1000)}, senseEnergyPoint{IntervalStart: to, Interval: "01:00:00", Value: senseNumber(1000)})
		total, complete := sumSenseEnergy(points, from, to)
		if !complete || total != float64(tt.hours) {
			t.Fatalf("%s: total=%v complete=%v; expected %d hours", from, total, complete, tt.hours)
		}
		if _, complete := sumSenseEnergy(points[1:], from, to); complete {
			t.Fatal("published a partial day as complete")
		}
		if _, complete := sumSenseEnergy(append(points, points[0]), from, to); complete {
			t.Fatal("double-counted a duplicated hour")
		}
	}
}

func TestSenseMidnightDoesNotRelabelPreviousDaysAsToday(t *testing.T) {
	zone := stockholm(t)
	before := time.Date(2026, 11, 7, 23, 58, 0, 0, zone)
	client := testAPI(func(r *http.Request) (*http.Response, error) { return testResponse(200, senseFixture(t, before)), nil })
	s := &senseStore{}
	if err := s.refresh(context.Background(), client, chargerUUID, zone, before); err != nil {
		t.Fatal(err)
	}
	metrics := senseMetrics(s, zone, before.Add(3*time.Minute))
	if strings.Contains(metrics, "zaptec_sense_total_energy_kwh{") || strings.Contains(metrics, "zaptec_sense_charge_energy_kwh{") {
		t.Fatal("relabeled previous snapshot as complete energy for a different day")
	}
}

func TestSenseRejectsMissingReadingAndMalformedEnergy(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"LastReading":null}`, `{"LastReading":{"ObservedAt":"2026-11-07T12:00:00Z"},"TotalEnergy":[{"IntervalStart":"2026-11-07T00:00:00Z","Interval":"01:00:00"}]}`,
	} {
		client := testAPI(func(r *http.Request) (*http.Response, error) { return testResponse(200, body), nil })
		if _, err := client.energySensorData(context.Background(), chargerUUID, historyFrom, historyNow); err == nil {
			t.Fatal("accepted invalid Sense data")
		}
	}
}

func TestSenseBackoffAndRetryAfter(t *testing.T) {
	interval := 5 * time.Minute
	serverError := &apiHTTPError{StatusCode: 500}
	if got := senseRetryDelay(interval, interval, serverError); got != 10*time.Minute {
		t.Fatalf("first failure delay %s", got)
	}
	if got := senseRetryDelay(interval, 40*time.Minute, serverError); got != time.Hour {
		t.Fatalf("backoff cap %s", got)
	}
	if got := senseRetryDelay(interval, time.Hour, nil); got != interval {
		t.Fatalf("success did not reset polling: %s", got)
	}
	limited := fmt.Errorf("wrapped: %w", &apiHTTPError{StatusCode: 429, RetryAfter: 2 * time.Hour})
	if got := senseRetryDelay(interval, interval, limited); got != 2*time.Hour {
		t.Fatalf("ignored Retry-After: %s", got)
	}
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") != "zaptec-exporter/dev" {
			t.Fatal("missing application User-Agent")
		}
		response := testResponse(429, `{}`)
		response.Header.Set("Retry-After", "3600")
		return response, nil
	})
	_, err := client.snapshot(context.Background(), chargerUUID)
	var status *apiHTTPError
	if !errors.As(err, &status) || status.StatusCode != 429 || status.RetryAfter != time.Hour {
		t.Fatalf("lost HTTP backoff information: %v", err)
	}
}
