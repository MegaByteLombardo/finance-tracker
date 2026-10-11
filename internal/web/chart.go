package web

import (
	"math/big"
	"strconv"
	"strings"
	"time"
)

// The net-worth chart is an inline SVG built here, on the server (TD-070).
//
// This is GEOMETRY, not financial arithmetic: it decides where on a 1000-by-240
// canvas a point sits, from integer minor units, with integer arithmetic. No
// figure computed here is ever shown as money -- every label comes from a
// TotalFormatted the backend produced -- and no float is involved anywhere,
// which is also why the canvas is large: an integer coordinate on it is finer
// than a screen pixel.

const (
	chartWidth   = 1000
	chartHeight  = 240
	chartPadTop  = 16
	chartPadBase = 16
)

// ChartView is a drawn series, ready for a template.
type ChartView struct {
	Width  int
	Height int

	// Segments are the unbroken runs of complete days. A day whose total is
	// incomplete is a GAP between segments, never a point (TD-067).
	Segments []ChartSegment

	// Days is every day of the range, in order, for the tooltip: where it
	// sits and what it says. It is embedded in the page as JSON and read by
	// chart_tooltip.js, which positions things and never computes a figure.
	Days []ChartDay

	// Last is the most recent complete point, emphasised on the chart.
	Last    ChartMarker
	HasLast bool

	// ZeroY is where zero sits, when the range crosses it.
	ZeroY       int
	HasZeroLine bool

	// MaxLabel and MinLabel are the highest and lowest complete totals, as the
	// backend formatted them.
	MaxLabel string
	MinLabel string

	StartLabel string
	EndLabel   string

	// HasData is false when no day in the range could be valued.
	HasData bool

	// GapDays counts the incomplete days, so the template can say so.
	GapDays int
}

// ChartSegment is one unbroken run, as SVG path data.
type ChartSegment struct {
	Line string
	Area string

	// Dot is true for a run of a single day, which has no length to stroke and
	// is drawn as a dot instead.
	Dot  bool
	DotX int
	DotY int
}

// ChartDay is one day as the tooltip shows it. An incomplete day has no Y and
// no Value: the tooltip says it could not be valued, exactly as the chart
// leaves a gap there.
type ChartDay struct {
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Date     string `json:"d"`
	Value    string `json:"v,omitempty"`
	Complete bool   `json:"ok"`
}

// ChartMarker is one emphasised point.
type ChartMarker struct {
	X     int
	Y     int
	Label string
}

// BuildNetWorthChart lays out a series. currencyCode is only appended to the
// labels.
func BuildNetWorthChart(points []NetWorthPoint, currencyCode string) ChartView {
	view := ChartView{Width: chartWidth, Height: chartHeight}
	if len(points) == 0 {
		return view
	}
	view.StartLabel = shortSpanishDate(points[0].Date)
	view.EndLabel = shortSpanishDate(points[len(points)-1].Date)

	var (
		minPoint, maxPoint NetWorthPoint
		found              bool
	)
	for _, p := range points {
		if !p.Complete {
			view.GapDays++
			continue
		}
		if !found || p.TotalMinor < minPoint.TotalMinor {
			minPoint = p
		}
		if !found || p.TotalMinor > maxPoint.TotalMinor {
			maxPoint = p
		}
		found = true
	}
	if !found {
		return view
	}
	view.HasData = true
	view.MaxLabel = formatAmountString(maxPoint.TotalFormatted) + " " + currencyCode
	view.MinLabel = formatAmountString(minPoint.TotalFormatted) + " " + currencyCode

	x := func(i int) int {
		if len(points) == 1 {
			return chartWidth / 2
		}
		return i * chartWidth / (len(points) - 1)
	}
	y := func(v int64) int { return scaleY(v, minPoint.TotalMinor, maxPoint.TotalMinor) }

	if minPoint.TotalMinor < 0 && maxPoint.TotalMinor > 0 {
		view.HasZeroLine = true
		view.ZeroY = y(0)
	}

	var run []int
	flush := func() {
		if len(run) == 0 {
			return
		}
		view.Segments = append(view.Segments, segmentFor(run, points, x, y))
		run = run[:0]
	}

	view.Days = make([]ChartDay, 0, len(points))
	for i, p := range points {
		day := ChartDay{X: x(i), Date: shortSpanishDate(p.Date)}
		if !p.Complete {
			view.Days = append(view.Days, day)
			flush()
			continue
		}
		run = append(run, i)

		day.Y = y(p.TotalMinor)
		day.Value = formatAmountString(p.TotalFormatted) + " " + currencyCode
		day.Complete = true
		view.Days = append(view.Days, day)

		view.Last = ChartMarker{X: day.X, Y: day.Y, Label: day.Date + " · " + day.Value}
		view.HasLast = true
	}
	flush()
	return view
}

// scaleY maps a value onto the canvas, highest at the top. The product is taken
// on big.Int because a balance in minor units times the canvas height can
// exceed int64 for a large enough ledger.
func scaleY(v, lowest, highest int64) int {
	usable := int64(chartHeight - chartPadTop - chartPadBase)
	if highest == lowest {
		return chartHeight / 2
	}
	offset := new(big.Int).Sub(big.NewInt(highest), big.NewInt(v))
	offset.Mul(offset, big.NewInt(usable))
	offset.Quo(offset, new(big.Int).Sub(big.NewInt(highest), big.NewInt(lowest)))
	return chartPadTop + int(offset.Int64())
}

func segmentFor(run []int, points []NetWorthPoint, x func(int) int, y func(int64) int) ChartSegment {
	if len(run) == 1 {
		i := run[0]
		return ChartSegment{Dot: true, DotX: x(i), DotY: y(points[i].TotalMinor)}
	}

	var line strings.Builder
	for k, i := range run {
		if k == 0 {
			line.WriteString("M")
		} else {
			line.WriteString(" L")
		}
		line.WriteString(strconv.Itoa(x(i)))
		line.WriteByte(' ')
		line.WriteString(strconv.Itoa(y(points[i].TotalMinor)))
	}

	first, last := x(run[0]), x(run[len(run)-1])
	base := strconv.Itoa(chartHeight)
	area := line.String() + " L" + strconv.Itoa(last) + " " + base + " L" + strconv.Itoa(first) + " " + base + " Z"
	return ChartSegment{Line: line.String(), Area: area}
}

var spanishShortMonths = [...]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

// shortSpanishDate renders a calendar date as "5 oct 2026". The date is read
// as stored -- UTC midnight naming a Venezuelan calendar day -- and is NOT moved
// into the economic zone, which would turn it into the evening before.
func shortSpanishDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.Itoa(t.Day()) + " " + spanishShortMonths[t.Month()-1] + " " + strconv.Itoa(t.Year())
}
