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

	for _, m := range chart.Markers {
		if m.X < 0 || m.X > chartWidth || m.Y < 0 || m.Y > chartHeight {
			t.Errorf("marker %+v is off the %dx%d canvas", m, chartWidth, chartHeight)
		}
	}
	if chart.Markers[0].X != 0 || chart.Markers[2].X != chartWidth {
		t.Errorf("x = %d..%d, want the full width", chart.Markers[0].X, chart.Markers[2].X)
	}
	// Highest at the top, lowest at the bottom.
	if chart.Markers[2].Y != chartPadTop || chart.Markers[0].Y != chartHeight-chartPadBase {
		t.Errorf("y = %d (max) and %d (min), want %d and %d",
			chart.Markers[2].Y, chart.Markers[0].Y, chartPadTop, chartHeight-chartPadBase)
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
	for _, m := range chart.Markers {
		if m.Y != chartHeight/2 {
			t.Errorf("a flat series point is at y=%d, want %d", m.Y, chartHeight/2)
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

func TestALongSeriesHasBoundedHoverTargets(t *testing.T) {
	t.Parallel()

	points := make([]NetWorthPoint, 0, 1826)
	start := day(1)
	for i := 0; i < 1826; i++ {
		points = append(points, NetWorthPoint{Date: start.AddDate(0, 0, i), TotalMinor: int64(i), TotalFormatted: "x", Complete: true})
	}
	chart := BuildNetWorthChart(points, "USD")
	if len(chart.Markers) > chartMaxMarkers+1 {
		t.Errorf("%d markers for five years, want at most %d", len(chart.Markers), chartMaxMarkers+1)
	}
	if last := chart.Markers[len(chart.Markers)-1]; last.X != chartWidth {
		t.Errorf("the last day is not a marker: %+v", last)
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
