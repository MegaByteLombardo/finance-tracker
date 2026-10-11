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
	}, "USD", 2)

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
	}, "USD", 2)

	for _, d := range chart.Days {
		if d.X < 0 || d.X > chartWidth || d.Y < 0 || d.Y > chartHeight {
			t.Errorf("day %+v is off the %dx%d canvas", d, chartWidth, chartHeight)
		}
	}
	if chart.Days[0].X != 0 || chart.Days[2].X != chartWidth {
		t.Errorf("x = %d..%d, want the full width", chart.Days[0].X, chart.Days[2].X)
	}
	// Highest towards the top, lowest towards the bottom, zero between them.
	// (-50.00 against 9e15 is not a pixel away from zero, hence <=.)
	if !(chart.Days[2].Y < chart.Days[1].Y && chart.Days[1].Y <= chart.Days[0].Y) {
		t.Errorf("y = %d, %d, %d; want the max above zero above the min",
			chart.Days[2].Y, chart.Days[1].Y, chart.Days[0].Y)
	}
	if chart.Days[1].Y != chart.ZeroY {
		t.Errorf("the zero day is at y=%d, but zero is at %d", chart.Days[1].Y, chart.ZeroY)
	}
	if !chart.HasZeroLine {
		t.Error("a range crossing zero has no zero line")
	}
	if strings.ContainsAny(chart.Segments[0].Line, ".eE") {
		t.Errorf("path %q is not made of integers", chart.Segments[0].Line)
	}
}

func TestAnAllZeroSeriesLiesOnTheZeroLine(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{point(1, 0, "0.00", true), point(2, 0, "0.00", true)}, "USD", 2)
	for _, d := range chart.Days {
		if d.Y != chartHeight {
			t.Errorf("a zero point is at y=%d, want the foot of the chart, %d", d.Y, chartHeight)
		}
	}
	if len(chart.YTicks) < 2 {
		t.Errorf("y ticks = %+v, want a scale above zero", chart.YTicks)
	}
}

// TestTheScaleStartsAtZero is the point of the axis: 9,500 against 10,000 must
// look like a 5% dip, not a fall to the floor.
func TestTheScaleStartsAtZero(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{
		point(1, 1_000_000, "10000.00", true),
		point(2, 950_000, "9500.00", true),
	}, "USD", 2)

	var labels []string
	for _, tick := range chart.YTicks {
		labels = append(labels, tick.Label)
	}
	if got := strings.Join(labels, " "); got != "0 5,000 10,000" {
		t.Errorf("y ticks = %q, want \"0 5,000 10,000\"", got)
	}
	if first := chart.YTicks[0]; !first.Zero || first.Y != chartHeight || first.TopPercent != "100.00%" {
		t.Errorf("first tick = %+v, want zero at the foot", first)
	}
	if chart.HasZeroLine {
		t.Error("zero is the foot of an all-positive chart, not a line across it")
	}
	// The dip is about 5% of the height, not all of it.
	if dip := chart.Days[1].Y - chart.Days[0].Y; dip < 5 || dip > 20 {
		t.Errorf("a 5%% dip moves the line %d units on a %d-unit canvas", dip, chartHeight)
	}
	// The area is filled down to zero.
	if !strings.HasSuffix(chart.Segments[0].Area, " 240 Z") {
		t.Errorf("area %q is not closed at zero", chart.Segments[0].Area)
	}
}

func TestAxisLabelsKeepDecimalsOnlyWhenTheStepNeedsThem(t *testing.T) {
	t.Parallel()

	chart := BuildNetWorthChart([]NetWorthPoint{point(1, 0, "0.00", true), point(2, 300, "3.00", true)}, "USD", 2)
	if got := chart.YTicks[len(chart.YTicks)-1].Label; got != "3" {
		t.Errorf("top label = %q, want whole units for a step of 1.00", got)
	}
	chart = BuildNetWorthChart([]NetWorthPoint{point(1, 0, "0.00", true), point(2, 90, "0.90", true)}, "USD", 2)
	if got := chart.YTicks[1].Label; got != "0.50" {
		t.Errorf("second label = %q, want 0.50 for a step of 0.50", got)
	}
}

// TestTheXAxisLabelsSuitTheRange: every day for a week, Mondays for a month,
// month starts for a year; and a mark per day while they can be told apart.
func TestTheXAxisLabelsSuitTheRange(t *testing.T) {
	t.Parallel()

	series := func(days int) []NetWorthPoint {
		out := make([]NetWorthPoint, 0, days)
		start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -days+1)
		for i := 0; i < days; i++ {
			out = append(out, NetWorthPoint{Date: start.AddDate(0, 0, i), TotalMinor: 100, TotalFormatted: "1.00", Complete: true})
		}
		return out
	}
	labels := func(c ChartView) []string {
		var out []string
		for _, tick := range c.XTicks {
			out = append(out, tick.Label)
		}
		return out
	}

	week := BuildNetWorthChart(series(7), "USD", 2)
	var week7 []string
	for _, tick := range week.XTicks {
		week7 = append(week7, tick.Label+" "+tick.Sub)
	}
	if got := strings.Join(week7, ","); got != "vie 25,sáb 26,dom 27,lun 28,mar 29,mié 30,jue 1" {
		t.Errorf("7 days: %q", got)
	}
	if week.XTicks[0].Align != "start" || week.XTicks[6].Align != "end" {
		t.Errorf("the end labels are not kept inside the plot: %+v", week.XTicks)
	}
	if strings.Count(week.DayTicks, "M") != 7 {
		t.Errorf("day marks = %q, want 7", week.DayTicks)
	}

	month := BuildNetWorthChart(series(30), "USD", 2)
	if got := strings.Join(labels(month), ","); got != "7 sep,14 sep,21 sep,28 sep" {
		t.Errorf("30 days: %q", got)
	}

	year := BuildNetWorthChart(series(365), "USD", 2)
	if got := strings.Join(labels(year), ","); got != "nov,dic,ene 26,feb,mar,abr,may,jun,jul,ago,sep,oct" {
		t.Errorf("365 days: %q", got)
	}
	if year.DayTicks != "" {
		t.Error("a year is marked day by day")
	}
}

func TestAChartWithNoValuableDayHasNoData(t *testing.T) {
	t.Parallel()

	if chart := BuildNetWorthChart(nil, "USD", 2); chart.HasData {
		t.Error("an empty series has data")
	}
	chart := BuildNetWorthChart([]NetWorthPoint{point(1, 0, "0.00", false)}, "USD", 2)
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
	}, "USD", 2)

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
