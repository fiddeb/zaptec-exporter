package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func testAPI(transport transportFunc) *apiClient {
	return &apiClient{http: &http.Client{Transport: transport}, token: "cached", expires: time.Now().Add(time.Hour)}
}

func TestRejectedTokenRecovery(t *testing.T) {
	for _, tt := range []struct {
		name              string
		status            int
		stillRejected     bool
		wantError         bool
		wantGET, wantAuth int
	}{
		{"refresh and retry", 401, false, false, 2, 1},
		{"stop after one retry", 401, true, true, 2, 1},
		{"do not retry forbidden", 403, false, true, 1, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gets, auth := 0, 0
			client := testAPI(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/oauth/token" {
					auth++
					return testResponse(200, `{"access_token":"fresh","expires_in":3600}`), nil
				}
				gets++
				if r.Header.Get("Authorization") == "Bearer cached" || tt.stillRejected {
					return testResponse(tt.status, `{}`), nil
				}
				return testResponse(200, `[{"stateId":513,"valueAsString":"7400"}]`), nil
			})
			items, err := client.snapshot(context.Background(), "charger")
			if (err != nil) != tt.wantError || gets != tt.wantGET || auth != tt.wantAuth {
				t.Fatalf("error=%v, GETs=%d, token requests=%d", err, gets, auth)
			}
			if !tt.wantError && (len(items) != 1 || items[0].Value != "7400") {
				t.Fatalf("retry did not decode snapshot: %+v", items)
			}
		})
	}
}

func TestRejectedOldTokenDoesNotInvalidateConcurrentRefresh(t *testing.T) {
	var client *apiClient
	requests := 0
	client = testAPI(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Authorization") == "Bearer cached" {
			client.mu.Lock()
			client.token = "concurrently-refreshed"
			client.mu.Unlock()
			return testResponse(401, `{}`), nil
		}
		if r.URL.Path == "/oauth/token" || r.Header.Get("Authorization") != "Bearer concurrently-refreshed" {
			t.Fatal("discarded concurrently refreshed token")
		}
		return testResponse(200, `[]`), nil
	})
	if _, err := client.snapshot(context.Background(), "charger"); err != nil || requests != 2 {
		t.Fatalf("error=%v, requests=%d", err, requests)
	}
}
