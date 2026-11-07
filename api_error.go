package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type apiHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *apiHTTPError) Error() string { return fmt.Sprintf("Zaptec API: HTTP %d", e.StatusCode) }

func retryAfter(header string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(header, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(header); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
