package web

import (
	"strings"
	"testing"
	"time"
)

func day(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

func point(d int, minor int64, formatted string, complete bool) NetWorthPoint {
	return NetWorthPoint{Date: day(d), TotalMinor: minor, TotalFormatted: formatted, Complete: complete}
}

func TestTheChartBreaksAtEveryIncompleteDay(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{
		point(1, 10000, "100.00", true),
		point(2, 20000, "200.00", true),
		point(3, 0, "0.00", false), // a gap, not a zero
		point(4, 15000, "150.00", true),
		point(5, 0, "0.00", false),
		point(6, 12000, "120.00", true),
		point(7, 13000, "130.00", true),
	}, "USD")

	if !chart.HasData || chart.GapDays != 2 {
		t.Fatalf("HasData %v, GapDays %d; want true and 2", chart.HasData, chart.GapDays)
	}
	if len(chart.Segments) != 3 {
		t.Fatalf("%d segments, want 3: [1-2], [4] as a dot, [6-7]", len(chart.Segments))
	}
	if chart.Segments[0].Dot || !chart.Segments[1].Dot || chart.Segments[2].Dot {
		t.Errorf("segments = %+v; only the single-day run is a dot", chart.Segments)
	}
	// The incomplete day's "0.00" is never the minimum: it is not a value.
	if chart.MinLabel != "100.00 USD" || chart.MaxLabel != "200.00 USD" {
		t.Errorf("labels = %q / %q, want 100.00 USD / 200.00 USD", chart.MinLabel, chart.MaxLabel)
	}
	if !chart.HasLast || chart.Last.Label != "7 sep 2026 · 130.00 USD" {
		t.Errorf("last = %+v", chart.Last)
	}
}

func TestTheChartGeometryStaysOnTheCanvas(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{
		point(1, -5000, "-50.00", true),
		point(2, 0, "0.00", true),
		point(3, 922337203685477580, "9223372036854775.80", true), // near int64's limit
	}, "USD")

	for _, d := range chart.Days {
		if d.X < 0 || d.X > chartWidth || d.Y < 0 || d.Y > chartHeight {
			t.Errorf("day %+v is off the %dx%d canvas", d, chartWidth, chartHeight)
		}
	}
	if chart.Days[0].X != 0 || chart.Days[2].X != chartWidth {
		t.Errorf("x = %d..%d, want the full width", chart.Days[0].X, chart.Days[2].X)
	}
	// Highest at the top, lowest at the bottom.
	if chart.Days[2].Y != chartPadTop || chart.Days[0].Y != chartHeight-chartPadBase {
		t.Errorf("y = %d (max) and %d (min), want %d and %d",
			chart.Days[2].Y, chart.Days[0].Y, chartPadTop, chartHeight-chartPadBase)
	}
	if !chart.HasZeroLine {
		t.Error("a range crossing zero has no zero line")
	}
	if strings.ContainsAny(chart.Segments[0].Line, ".eE") {
		t.Errorf("path %q is not made of integers", chart.Segments[0].Line)
	}
}

func TestAFlatSeriesIsDrawnAcrossTheMiddle(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{point(1, 0, "0.00", true), point(2, 0, "0.00", true)}, "USD")
	for _, d := range chart.Days {
		if d.Y != chartHeight/2 {
			t.Errorf("a flat series point is at y=%d, want %d", d.Y, chartHeight/2)
		}
	}
}

func TestAChartWithNoValuableDayHasNoData(t *testing.T) {
	t.Parallel()

	if chart := BuildNetWorthChart(nil, "USD"); chart.HasData {
		t.Error("an empty series has data")
	}
	chart := BuildNetWorthChart([]NetWorthPoint{point(1, 0, "0.00", false)}, "USD")
	if chart.HasData || chart.GapDays != 1 || len(chart.Segments) != 0 {
		t.Errorf("an all-gap series = %+v, want no data, one gap, nothing drawn", chart)
	}
}

// TestTheTooltipHasEveryDayIncludingGaps: one entry per day, in order. A gap
// day is listed, so the tooltip can say it could not be valued, but it carries
// no value and no height -- it is never drawn as a point.
func TestTheTooltipHasEveryDayIncludingGaps(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{
		point(1, 10000, "100.00", true),
		point(2, 0, "0.00", false),
		point(3, 20000, "200.00", true),
	}, "USD")

	if len(chart.Days) != 3 {
		t.Fatalf("%d days, want 3", len(chart.Days))
	}
	if d := chart.Days[0]; !d.Complete || d.Value != "100.00 USD" || d.Date != "1 sep 2026" {
		t.Errorf("day 1 = %+v, want complete, 100.00 USD on 1 sep 2026", d)
	}
	if d := chart.Days[1]; d.Complete || d.Value != "" || d.Y != 0 || d.X != chartWidth/2 {
		t.Errorf("day 2 = %+v, want an incomplete day at mid-width with no value", d)
	}
}

func TestParseDashboardPreset(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]DashboardPreset{
		"": DashboardPreset30D, "7d": DashboardPreset7D, "90D": DashboardPreset90D,
		" 1y ": DashboardPreset1Y, "all": DashboardPresetAll, "bogus": DashboardPreset30D,
	} {
		if got := ParseDashboardPreset(raw); got != want {
			t.Errorf("ParseDashboardPreset(%q) = %q, want %q", raw, got, want)
		}
	}
}
