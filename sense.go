package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/url"
	"sync"
	"time"
	_ "time/tzdata"
)

// Optional fields distinguish a real zero from a missing measurement.
type senseReading struct {
	ObservedAt             time.Time `json:"ObservedAt"`
	CurrentPhase1          *float64  `json:"CurrentPhase1"`
	CurrentPhase2          *float64  `json:"CurrentPhase2"`
	CurrentPhase3          *float64  `json:"CurrentPhase3"`
	VoltagePhase1          *float64  `json:"VoltagePhase1"`
	VoltagePhase2          *float64  `json:"VoltagePhase2"`
	VoltagePhase3          *float64  `json:"VoltagePhase3"`
	ChargeCurrentPhase1    *float64  `json:"ChargeCurrentPhase1"`
	ChargeCurrentPhase2    *float64  `json:"ChargeCurrentPhase2"`
	ChargeCurrentPhase3    *float64  `json:"ChargeCurrentPhase3"`
	AvailableCurrentPhase1 *float64  `json:"AvailableCurrentPhase1"`
	AvailableCurrentPhase2 *float64  `json:"AvailableCurrentPhase2"`
	AvailableCurrentPhase3 *float64  `json:"AvailableCurrentPhase3"`
	CurrentNeutral         *float64  `json:"CurrentNeutral"`
}

type senseEnergyPoint struct {
	IntervalStart time.Time `json:"IntervalStart"`
	Interval      string    `json:"Interval"`
	Value         *float64  `json:"Value"`
}

type sensePowerPoint struct {
	Timestamp time.Time `json:"Timestamp"`
	Power     *float64  `json:"Power"`
}

type senseData struct {
	LastReading       *senseReading      `json:"LastReading"`
	TotalEnergy       []senseEnergyPoint `json:"TotalEnergy"`
	ChargeEnergy      []senseEnergyPoint `json:"ChargeEnergy"`
	Readings          []sensePowerPoint  `json:"Readings"`
	PowerLimit        *float64           `json:"PowerLimit"`
	DynamicPowerLimit *float64           `json:"DynamicPowerLimit"`
}

func validSenseNumber(value *float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0))
}

func (c *apiClient) energySensorData(ctx context.Context, installationID string, from, to time.Time) (senseData, error) {
	query := url.Values{
		"from":       {from.UTC().Format(time.RFC3339Nano)},
		"to":         {to.UTC().Format(time.RFC3339Nano)},
		"resolution": {"day1"},
	}
	var data senseData
	if err := c.get(ctx, "/api/installation/"+url.PathEscape(installationID)+"/energySensorData?"+query.Encode(), &data); err != nil {
		return senseData{}, err
	}
	if data.LastReading == nil || data.LastReading.ObservedAt.IsZero() || data.LastReading.ObservedAt.After(to.Add(time.Minute)) {
		return senseData{}, errors.New("Sense response lacks a valid LastReading.ObservedAt")
	}
	r := data.LastReading
	for _, value := range []*float64{
		r.CurrentPhase1, r.CurrentPhase2, r.CurrentPhase3,
		r.VoltagePhase1, r.VoltagePhase2, r.VoltagePhase3,
		r.ChargeCurrentPhase1, r.ChargeCurrentPhase2, r.ChargeCurrentPhase3,
		r.AvailableCurrentPhase1, r.AvailableCurrentPhase2, r.AvailableCurrentPhase3,
		r.CurrentNeutral, data.PowerLimit, data.DynamicPowerLimit,
	} {
		if !validSenseNumber(value) {
			return senseData{}, errors.New("Sense response contains a non-finite measurement")
		}
	}
	for _, series := range [][]senseEnergyPoint{data.TotalEnergy, data.ChargeEnergy} {
		for _, point := range series {
			if point.IntervalStart.IsZero() || point.Interval != "01:00:00" || point.Value == nil || !validSenseNumber(point.Value) {
				return senseData{}, errors.New("Sense response contains an invalid hourly energy interval")
			}
		}
	}
	for _, point := range data.Readings {
		if point.Timestamp.IsZero() || point.Power == nil || !validSenseNumber(point.Power) {
			return senseData{}, errors.New("Sense response contains an invalid power reading")
		}
	}
	return data, nil
}

type senseSnapshot struct {
	installationID string
	fetchedAt      time.Time
	data           senseData
}

type senseStore struct {
	mu       sync.RWMutex
	snapshot *senseSnapshot // Replaced as a whole; published snapshots are immutable.
	fetchOK  bool
}

func senseDayStart(now time.Time, location *time.Location) time.Time {
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func (s *senseStore) recordFailure() {
	s.mu.Lock()
	s.fetchOK = false
	s.mu.Unlock()
}

func (s *senseStore) refresh(ctx context.Context, client *apiClient, installationID string, location *time.Location, now time.Time) error {
	from := senseDayStart(now, location).AddDate(0, 0, -1)
	data, err := client.energySensorData(ctx, installationID, from, now)
	if err != nil {
		s.recordFailure()
		return err
	}
	s.mu.Lock()
	s.snapshot = &senseSnapshot{installationID: installationID, fetchedAt: now, data: data}
	s.fetchOK = true
	s.mu.Unlock()
	return nil
}

func senseRetryDelay(interval, previous time.Duration, err error) time.Duration {
	if err == nil {
		return interval
	}
	delay := time.Hour
	if previous < time.Hour/2 {
		delay = 2 * previous
	}
	if delay < interval {
		delay = interval
	}
	var status *apiHTTPError
	if errors.As(err, &status) && status.RetryAfter > delay {
		delay = status.RetryAfter
	}
	return delay
}

func (s *senseStore) poll(ctx context.Context, client *apiClient, chargerID string, interval time.Duration, location *time.Location) {
	installationID := ""
	delay := interval
	for {
		if ctx.Err() != nil {
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var err error
		if installationID == "" {
			installationID, err = client.installationID(requestCtx, chargerID)
		}
		if err == nil {
			err = s.refresh(requestCtx, client, installationID, location, time.Now())
		} else {
			s.recordFailure()
		}
		cancel()
		delay = senseRetryDelay(interval, delay, err)
		if err != nil {
			log.Printf("Sense fetch error: %v; next attempt in %s", err, delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Only publish an energy sum if every hourly slot in the requested range exists.
// An incomplete final hour is included for today's running total.
func sumSenseEnergy(points []senseEnergyPoint, from, to time.Time) (float64, bool) {
	if !from.Before(to) {
		return 0, false
	}
	values := make(map[time.Time]float64)
	for _, point := range points {
		start := point.IntervalStart.UTC()
		if start.Before(from) || !start.Before(to) {
			continue
		}
		if _, duplicate := values[start]; duplicate || point.Value == nil || point.Interval != "01:00:00" {
			return 0, false
		}
		values[start] = *point.Value
	}
	var total float64
	for slot := from.UTC(); slot.Before(to); slot = slot.Add(time.Hour) {
		value, ok := values[slot]
		if !ok {
			return 0, false
		}
		total += value
	}
	return total, true
}
