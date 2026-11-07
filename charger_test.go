package main

import (
	"context"
	"net/http"
	"testing"
)

const chargerUUID = "3fa85f64-5717-4562-b3fc-2c963f66afa6"

func TestResolveChargerUUIDDoesNotListChargers(t *testing.T) {
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		t.Fatal("UUID configuration must not require charger-list access")
		return nil, nil
	})
	id, err := client.resolveChargerID(context.Background(), " 3FA85F64-5717-4562-B3FC-2C963F66AFA6 ")
	if err != nil || id != chargerUUID {
		t.Fatalf("id=%q, error=%v", id, err)
	}
}

func TestResolveSerialFollowsPagesAndMatchesDeviceID(t *testing.T) {
	calls := 0
	client := testAPI(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chargers" || r.URL.Query().Get("PageSize") != "100" {
			t.Fatalf("unexpected lookup request: %s", r.URL)
		}
		page := r.URL.Query().Get("PageIndex")
		calls++
		if page == "0" {
			// Neither a matching name nor a partial serial is a valid match.
			return testResponse(200, `{"pages":2,"data":[{"id":"11111111-1111-1111-1111-111111111111","deviceId":"ZAG000001-extra","name":"ZAG000001"}]}`), nil
		}
		if page != "1" {
			t.Fatalf("unexpected page: %s", page)
		}
		return testResponse(200, `{"pages":2,"data":[{"id":"`+chargerUUID+`","deviceId":"ZAG000001"}]}`), nil
	})
	id, err := client.resolveChargerID(context.Background(), " zag000001 ")
	if err != nil || id != chargerUUID || calls != 2 {
		t.Fatalf("id=%q, calls=%d, error=%v", id, calls, err)
	}
	// Both source endpoints must receive the resolved UUID, never the serial.
	client = testAPI(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/chargers/" + chargerUUID + "/state":
			return testResponse(200, `[]`), nil
		case "/api/sessions/archived":
			if r.URL.Query().Get("ChargerId") != chargerUUID {
				t.Fatal("archive received unresolved serial")
			}
			return testResponse(200, `{"sessions":[],"hasMore":false}`), nil
		default:
			t.Fatalf("unresolved source path: %s", r.URL)
			return nil, nil
		}
	})
	if _, err := client.snapshot(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := client.archivedSessions(context.Background(), id, historyFrom, historyNow); err != nil {
		t.Fatal(err)
	}
}

func TestResolveChargerRejectsAmbiguityMissingAndInvalidResults(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"no chargers", 200, `{"pages":0,"data":null}`},
		{"no matching serial", 200, `{"pages":1,"data":[{"id":"` + chargerUUID + `","deviceId":"another-serial"}]}`},
		{"missing pagination", 200, `{"data":[]}`},
		{"invalid UUID", 200, `{"pages":1,"data":[{"id":"ZAG000001","deviceId":"ZAG000001"}]}`},
		{"ambiguous serial", 200, `{"pages":1,"data":[{"id":"` + chargerUUID + `","deviceId":"ZAG000001"},{"id":"11111111-1111-1111-1111-111111111111","deviceId":"ZAG000001"}]}`},
		{"forbidden", 403, `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := testAPI(func(r *http.Request) (*http.Response, error) {
				return testResponse(tt.status, tt.body), nil
			})
			if _, err := client.resolveChargerID(context.Background(), "ZAG000001"); err == nil {
				t.Fatal("accepted an unresolvable charger")
			}
		})
	}
}
