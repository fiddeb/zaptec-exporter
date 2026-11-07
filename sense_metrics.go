package main

import (
	"fmt"
	"io"
	"strconv"
	"time"
)

func senseGauge(w io.Writer, name, help, labels string, value float64) {
	metricHeader(w, name, help)
	if labels != "" {
		fmt.Fprintf(w, "%s{%s} %g\n", name, labels, value)
	} else {
		fmt.Fprintf(w, "%s %g\n", name, value)
	}
}

func (s *senseStore) metrics(w io.Writer, interval time.Duration, location *time.Location, now time.Time) {
	s.mu.RLock()
	snapshot, fetchOK := s.snapshot, s.fetchOK
	s.mu.RUnlock()
	up := 0.0
	lastSuccess := 0.0
	if snapshot != nil {
		lastSuccess = float64(snapshot.fetchedAt.Unix())
		if fetchOK && now.Sub(snapshot.fetchedAt) <= 2*interval {
			up = 1
		}
	}
	senseGauge(w, "zaptec_exporter_sense_up", "Whether the latest Sense fetch succeeded and is recent", "", up)
	senseGauge(w, "zaptec_exporter_sense_last_success_timestamp_seconds", "Last successful Sense retrieval", "", lastSuccess)
	if snapshot == nil {
		return
	}
	label := fmt.Sprintf("installation_id=%q", snapshot.installationID)
	r := snapshot.data.LastReading
	senseGauge(w, "zaptec_sense_reading_timestamp_seconds", "Source timestamp of the latest Sense phase reading", label, float64(r.ObservedAt.Unix()))
	if now.Sub(r.ObservedAt) <= 2*interval && now.Sub(snapshot.fetchedAt) <= 2*interval {
		for _, series := range []struct {
			name, help string
			values     [3]*float64
		}{
			{"zaptec_sense_current_amperes", "Installation current by phase", [3]*float64{r.CurrentPhase1, r.CurrentPhase2, r.CurrentPhase3}},
			{"zaptec_sense_voltage_volts", "Installation voltage by phase", [3]*float64{r.VoltagePhase1, r.VoltagePhase2, r.VoltagePhase3}},
			{"zaptec_sense_charge_current_amperes", "Charging current reported by Sense by phase", [3]*float64{r.ChargeCurrentPhase1, r.ChargeCurrentPhase2, r.ChargeCurrentPhase3}},
			{"zaptec_sense_available_current_amperes", "Available charging current by phase", [3]*float64{r.AvailableCurrentPhase1, r.AvailableCurrentPhase2, r.AvailableCurrentPhase3}},
		} {
			metricHeader(w, series.name, series.help)
			for phase, value := range series.values {
				if value != nil {
					fmt.Fprintf(w, "%s{%s,phase=%q} %g\n", series.name, label, strconv.Itoa(phase+1), *value)
				}
			}
		}
		if r.CurrentNeutral != nil {
			senseGauge(w, "zaptec_sense_neutral_current_amperes", "Installation neutral current", label, *r.CurrentNeutral)
		}
	}
	var latest *sensePowerPoint
	for _, point := range snapshot.data.Readings {
		if point.Timestamp.After(now) {
			continue
		}
		if latest == nil || point.Timestamp.After(latest.Timestamp) {
			copy := point
			latest = &copy
		}
	}
	if latest != nil && now.Sub(latest.Timestamp) <= 2*interval && now.Sub(snapshot.fetchedAt) <= 2*interval {
		senseGauge(w, "zaptec_sense_power_watts", "Latest installation power reading; converted from API kW", label, *latest.Power*1000)
		senseGauge(w, "zaptec_sense_power_reading_timestamp_seconds", "Source timestamp of the latest installation power reading", label, float64(latest.Timestamp.Unix()))
	}
	if snapshot.data.PowerLimit != nil {
		senseGauge(w, "zaptec_sense_power_limit_watts", "Installation power limit; converted from API kW", label, *snapshot.data.PowerLimit*1000)
	}
	if snapshot.data.DynamicPowerLimit != nil {
		senseGauge(w, "zaptec_sense_dynamic_power_limit_watts", "Dynamic installation power limit; converted from API kW", label, *snapshot.data.DynamicPowerLimit*1000)
	}
	writeSenseEnergy(w, snapshot, location, now, label)
}

func writeSenseEnergy(w io.Writer, snapshot *senseSnapshot, location *time.Location, now time.Time, label string) {
	today := senseDayStart(now, location)
	days := [2]struct {
		period   string
		from, to time.Time
	}{
		{"today", today, snapshot.fetchedAt},
		{"yesterday", today.AddDate(0, 0, -1), today},
	}
	type dailySum struct {
		value    float64
		complete bool
	}
	series := []struct {
		kind, name, help string
		points           []senseEnergyPoint
		sums             [2]dailySum
	}{
		{kind: "total", name: "zaptec_sense_total_energy_kwh", help: "Installation energy reported by Sense for each calendar day; today is a running total", points: snapshot.data.TotalEnergy},
		{kind: "charge", name: "zaptec_sense_charge_energy_kwh", help: "Charging energy reported by Sense for each calendar day; today is a running total", points: snapshot.data.ChargeEnergy},
	}
	for i := range series {
		for j, day := range days {
			value, complete := sumSenseEnergy(series[i].points, day.from, day.to)
			series[i].sums[j] = dailySum{value: value, complete: complete && !snapshot.fetchedAt.Before(day.to)}
		}
	}
	senseGauge(w, "zaptec_sense_energy_as_of_timestamp_seconds", "Retrieval time through which the running energy totals were requested", label, float64(snapshot.fetchedAt.Unix()))
	metricHeader(w, "zaptec_sense_day_start_timestamp_seconds", "Start of each calendar day in the configured Sense timezone")
	for _, day := range days {
		fmt.Fprintf(w, "zaptec_sense_day_start_timestamp_seconds{%s,period=%q} %d\n", label, day.period, day.from.Unix())
	}
	metricHeader(w, "zaptec_sense_energy_complete", "Whether all expected hourly slots were returned; today is a running total through the last fetch")
	for _, entry := range series {
		for j, day := range days {
			value := 0
			if entry.sums[j].complete {
				value = 1
			}
			fmt.Fprintf(w, "zaptec_sense_energy_complete{%s,kind=%q,period=%q} %d\n", label, entry.kind, day.period, value)
		}
	}
	for _, entry := range series {
		metricHeader(w, entry.name, entry.help)
		for j, day := range days {
			if entry.sums[j].complete {
				fmt.Fprintf(w, "%s{%s,period=%q} %g\n", entry.name, label, day.period, entry.sums[j].value)
			}
		}
	}
}
