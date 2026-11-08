package backup

import (
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, london)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	f := "2006-01-02 15:04 MST"
	for _, c := range []struct {
		name       string
		s          Schedule
		now        string
		next, prev string
	}{
		{"hourly", Schedule{Frequency: Hourly, Minute: 15}, "2026-11-08 10:20", "2026-11-08 11:15 GMT", "2026-11-08 10:15 GMT"},
		{"hourly on the minute", Schedule{Frequency: Hourly, Minute: 15}, "2026-11-08 10:15", "2026-11-08 11:15 GMT", "2026-11-08 10:15 GMT"},
		{"daily", Schedule{Frequency: Daily, Time: "02:00"}, "2026-11-08 10:00", "2026-11-09 02:00 GMT", "2026-11-08 02:00 GMT"},
		{"weekly", Schedule{Frequency: Weekly, Time: "02:00", DaysOfWeek: []string{"mon", "thu"}}, "2026-11-08 10:00", // a Sunday
			"2026-11-09 02:00 GMT", "2026-11-05 02:00 GMT"},
		{"monthly 31st in November", Schedule{Frequency: Monthly, Time: "23:30", DayOfMonth: 31}, "2026-11-08 10:00",
			"2026-11-30 23:30 GMT", "2026-10-31 23:30 GMT"},
		{"monthly Feb", Schedule{Frequency: Monthly, Time: "01:00", DayOfMonth: 30}, "2027-02-10 00:00",
			"2027-02-28 01:00 GMT", "2027-01-30 01:00 GMT"},
		// 2027-03-28: clocks go 01:00 GMT -> 02:00 BST; 01:30 doesn't exist.
		{"spring forward", Schedule{Frequency: Daily, Time: "01:30"}, "2027-03-28 00:30",
			"2027-03-28 02:30 BST", "2027-03-27 01:30 GMT"},
	} {
		now := at(c.now)
		n, ok := c.s.Next(now, london)
		p, ok2 := c.s.Prev(now, london)
		if !ok || !ok2 || n.Format(f) != c.next || p.Format(f) != c.prev {
			t.Errorf("%s: next %s (want %s), prev %s (want %s)", c.name, n.Format(f), c.next, p.Format(f), c.prev)
		}
	}
	// 2026-10-25: 01:00-02:00 happens twice. Go picks one instance; either
	// way the day has one run.
	daily := Schedule{Frequency: Daily, Time: "01:30"}
	n1, _ := daily.Next(at("2026-10-25 00:30"), london)
	n2, _ := daily.Next(n1, london)
	if n1.In(london).Day() != 25 || n2.In(london).Day() != 26 {
		t.Errorf("fall back: %s then %s", n1.Format(f), n2.Format(f))
	}
	if _, ok := (Schedule{Frequency: Disabled}).Next(time.Now(), london); ok {
		t.Error("disabled has a next run")
	}
	for _, s := range []Schedule{{Frequency: "yearly"}, {Frequency: Daily, Time: "2:00"}, {Frequency: Weekly, Time: "02:00"},
		{Frequency: Weekly, Time: "02:00", DaysOfWeek: []string{"funday"}}, {Frequency: Monthly, Time: "02:00", DayOfMonth: 0}, {Frequency: Hourly, Minute: 60}} {
		if f, _ := s.Validate(); f == "" {
			t.Errorf("%+v accepted", s)
		}
	}
}
