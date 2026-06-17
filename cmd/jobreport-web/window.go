package main

import (
	"fmt"
	"strconv"
	"time"
)

// buildWindow turns six UTC form fields into a Prometheus-style epoch range
// "<startEpoch>..<endEpoch>".
//
// If all six fields are empty it returns "" so the caller omits -window and
// jobreport falls back to its default 10-minute window. If both the start and
// end groups are fully provided they are parsed as UTC (date layout
// 2006-01-02, integer hour/minute) and the epoch range is returned. Partial
// input, a bad format, or an end that is not strictly after the start returns a
// descriptive error.
func buildWindow(startDate, startHour, startMin, endDate, endHour, endMin string) (string, error) {
	fields := []string{startDate, startHour, startMin, endDate, endHour, endMin}
	allEmpty := true
	for _, f := range fields {
		if f != "" {
			allEmpty = false
			break
		}
	}
	if allEmpty {
		return "", nil
	}

	start, err := parseUTCMoment("start", startDate, startHour, startMin)
	if err != nil {
		return "", err
	}
	end, err := parseUTCMoment("end", endDate, endHour, endMin)
	if err != nil {
		return "", err
	}

	if !end.After(start) {
		return "", fmt.Errorf("end time must be after start time")
	}

	return fmt.Sprintf("%d..%d", start.Unix(), end.Unix()), nil
}

// parseUTCMoment combines a date (2006-01-02), hour, and minute into a UTC time.
// All three components must be present and valid; label identifies the group in
// error messages.
func parseUTCMoment(label, date, hour, minute string) (time.Time, error) {
	if date == "" || hour == "" || minute == "" {
		return time.Time{}, fmt.Errorf("%s time is incomplete: date, hour, and minute are all required", label)
	}

	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s date %q: want YYYY-MM-DD", label, date)
	}

	h, err := strconv.Atoi(hour)
	if err != nil || h < 0 || h > 23 {
		return time.Time{}, fmt.Errorf("invalid %s hour %q: want 0-23", label, hour)
	}

	m, err := strconv.Atoi(minute)
	if err != nil || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("invalid %s minute %q: want 0-59", label, minute)
	}

	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.UTC), nil
}
