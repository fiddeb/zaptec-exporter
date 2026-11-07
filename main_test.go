package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestObservationAcceptsZaptecTimestampFormats(t *testing.T) {
	for _, tt := range []struct{ stamp, want string }{
		{"2026-11-07T08:44:26.173", "2026-11-07T08:44:26.173Z"},
		{"2026-11-07T08:44:26", "2026-11-07T08:44:26Z"},
		{"2026-11-07T08:44:26.1234567", "2026-11-07T08:44:26.1234567Z"},
		{"2026-11-07T08:44:26.173Z", "2026-11-07T08:44:26.173Z"},
		{"2026-11-07T09:44:26.173+01:00", "2026-11-07T08:44:26.173Z"},
	} {
		t.Run(tt.stamp, func(t *testing.T) {
			var item observation
			// Real REST responses use PascalCase; AMQP can use camelCase.
			payload := `{"ChargerId":"charger","StateId":513,"Timestamp":"` + tt.stamp + `","ValueAsString":"0.000"}`
			if err := json.Unmarshal([]byte(payload), &item); err != nil {
				t.Fatal(err)
			}
			if item.Timestamp.UTC().Format(time.RFC3339Nano) != tt.want || item.StateID != 513 || item.Value != "0.000" || item.ChargerID != "charger" {
				t.Fatalf("incorrect decoded observation: %+v", item)
			}
		})
	}
	var item observation
	if err := json.Unmarshal([]byte(`{"timestamp":"not-a-date"}`), &item); err == nil {
		t.Fatal("accepted invalid observation timestamp")
	}
}

func TestMetrics(t *testing.T) {
	s := newStore()
	now := time.Now().UTC()
	s.apply([]observation{{StateID: 513, Timestamp: now, Value: "7400"}, {StateID: 507, Timestamp: now, Value: "16"}})
	s.recordFetch(true)
	r := httptest.NewRecorder()
	s.metrics(r, "charger-1", 5*time.Minute)
	body := r.Body.String()
	for _, expect := range []string{"zaptec_exporter_up 1", "zaptec_charge_power_watts{charger_id=\"charger-1\"} 7400", "zaptec_charge_current_amperes{charger_id=\"charger-1\",phase=\"1\"} 16"} {
		if !strings.Contains(body, expect) {
			t.Errorf("missing %q", expect)
		}
	}
}
func TestRejectOldObservation(t *testing.T) {
	s := newStore()
	now := time.Now().UTC()
	s.apply([]observation{{StateID: 513, Timestamp: now, Value: "3000"}})
	s.apply([]observation{{StateID: 513, Timestamp: now.Add(-time.Minute), Value: "9999"}})
	if s.data[513].Value != "3000" {
		t.Fatal("older observation overwrote newer observation")
	}
}
func TestStaleNotExported(t *testing.T) {
	s := newStore()
	s.apply([]observation{{StateID: 513, Timestamp: time.Now().Add(-time.Hour), Value: "7400"}})
	r := httptest.NewRecorder()
	s.metrics(r, "charger", 5*time.Minute)
	if strings.Contains(r.Body.String(), "zaptec_charge_power_watts{") {
		t.Fatal("stale value exported")
	}
}

func TestMultiPhaseMetricsHaveOneHeaderAndGroupedSamples(t *testing.T) {
	s := newStore()
	for _, id := range []int{501, 502, 503, 507, 508, 509} {
		s.apply([]observation{{StateID: id, Timestamp: time.Now(), Value: "16"}})
	}
	r := httptest.NewRecorder()
	s.metrics(r, "charger", time.Minute)
	lines := strings.Split(r.Body.String(), "\n")
	for _, name := range []string{"zaptec_charge_current_amperes", "zaptec_charge_voltage_volts"} {
		for _, prefix := range []string{"# HELP ", "# TYPE "} {
			if n := strings.Count(r.Body.String(), prefix+name+" "); n != 1 {
				t.Errorf("%s%s: got %d declarations", prefix, name, n)
			}
		}
		for i, line := range lines {
			if line == "# TYPE "+name+" gauge" {
				for phase := 1; phase <= 3; phase++ {
					if i+phase >= len(lines) || !strings.HasPrefix(lines[i+phase], name+"{") {
						t.Fatalf("phase samples must immediately follow header: %s", r.Body.String())
					}
				}
			}
		}
	}
}
