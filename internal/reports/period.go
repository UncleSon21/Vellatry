// Package reports builds the CMO reports: frozen snapshots of stored results, the one
// HTML template that renders both the web view and the PDF, and the figure check that
// keeps a summary to numbers the report actually shows. It never calls an external
// service, so the api role renders reports with it and the worker hands its HTML to
// the PDF renderer.
package reports

import (
	"errors"
	"fmt"
	"time"
)

// Period rules.
const (
	Month     = "month"      // calendar month
	FYQuarter = "fy_quarter" // Australian financial-year quarter (Q1 is July to September)
	FY        = "fy"         // Australian financial year, 1 July to 30 June
	Custom    = "custom"     // dates chosen per report
)

// Period is an inclusive range of dates.
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Label string    `json:"label"`
}

// Days is the number of days in the period.
func (p Period) Days() int { return int(p.End.Sub(p.Start).Hours()/24) + 1 }

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// fyOf returns the calendar year a financial year ends in (FY27 ends in June 2027).
func fyOf(t time.Time) int {
	if t.Month() >= time.July {
		return t.Year() + 1
	}
	return t.Year()
}

// Containing returns the period of rule that contains day.
func Containing(rule string, day time.Time) (Period, error) {
	day = date(day.Year(), day.Month(), day.Day())
	switch rule {
	case Month:
		start := date(day.Year(), day.Month(), 1)
		end := start.AddDate(0, 1, -1)
		return Period{start, end, start.Format("January 2006")}, nil
	case FYQuarter:
		fy := fyOf(day)
		// Months since the FY began (July = 0).
		offset := (int(day.Month()) + 5) % 12
		q := offset/3 + 1
		start := date(fy-1, time.July, 1).AddDate(0, (q-1)*3, 0)
		end := start.AddDate(0, 3, -1)
		return Period{start, end, fmt.Sprintf("Q%d FY%02d (%s to %s)", q, fy%100, start.Format("Jan"), end.Format("Jan 2006"))}, nil
	case FY:
		fy := fyOf(day)
		start := date(fy-1, time.July, 1)
		return Period{start, date(fy, time.June, 30), fmt.Sprintf("FY%02d (July %d to June %d)", fy%100, fy-1, fy)}, nil
	}
	return Period{}, fmt.Errorf("reports: no automatic period for rule %q", rule)
}

// LatestComplete returns the newest period of rule that ended before today.
func LatestComplete(rule string, today time.Time) (Period, error) {
	cur, err := Containing(rule, today)
	if err != nil {
		return cur, err
	}
	return Containing(rule, cur.Start.AddDate(0, 0, -1))
}

// ErrBadRange is returned for a custom range that is empty, reversed or too long.
var ErrBadRange = errors.New("reports: the period must run forwards and be at most 366 days")

// CustomPeriod validates a custom range.
func CustomPeriod(start, end time.Time) (Period, error) {
	p := Period{Start: date(start.Year(), start.Month(), start.Day()), End: date(end.Year(), end.Month(), end.Day())}
	if p.End.Before(p.Start) || p.Days() > 366 {
		return p, ErrBadRange
	}
	if p.Start.Year() == p.End.Year() {
		p.Label = p.Start.Format("2 Jan") + " to " + p.End.Format("2 Jan 2006")
	} else {
		p.Label = p.Start.Format("2 Jan 2006") + " to " + p.End.Format("2 Jan 2006")
	}
	return p, nil
}

// Previous is the period a report compares against: the previous period of the same
// rule, or for a custom range the same number of days immediately before it.
func Previous(rule string, p Period) Period {
	if rule == Custom {
		n := p.Days()
		prev := Period{Start: p.Start.AddDate(0, 0, -n), End: p.Start.AddDate(0, 0, -1)}
		prev.Label = prev.Start.Format("2 Jan") + " to " + prev.End.Format("2 Jan 2006")
		return prev
	}
	prev, _ := Containing(rule, p.Start.AddDate(0, 0, -1))
	return prev
}
