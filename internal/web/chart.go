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
// total computed here is ever shown as money -- every point's label comes from
// a TotalFormatted the backend produced. The one set of numbers this file does
// produce is the vertical axis: round values (0, 1,000, 2,000...) that say
// where the gridlines are, not what anybody holds. No float is involved
// anywhere, which is also why the canvas is large: an integer coordinate on it
// is finer than a screen pixel.

const (
	chartWidth   = 1000
	chartHeight  = 240
	chartPadTop  = 8
	chartPadBase = 0

	// chartYIntervals is roughly how many bands the vertical axis is cut into.
	chartYIntervals = 4

	// chartDayTicksMax is the longest range that gets a mark for every day.
	// Beyond it the marks would be closer together than a pixel.
	chartDayTicksMax = 92
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

	// YTicks are the horizontal gridlines, lowest first. The axis always
	// includes zero: a scale that starts wherever the data happens to start
	// turns a 5% dip into a fall off a cliff.
	YTicks []ChartYTick

	// ZeroY is where zero sits. HasZeroLine is true when zero is inside the
	// range rather than at its foot, which happens once a total is negative.
	ZeroY       int
	HasZeroLine bool

	// XTicks are the labelled days on the horizontal axis.
	XTicks []ChartXTick

	// DayTicks is SVG path data for one small mark per day, empty for ranges
	// too long to mark every day.
	DayTicks string

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

// ChartYTick is one horizontal gridline. TopPercent places its label beside
// the plot, as a CSS percentage of the plot's height.
type ChartYTick struct {
	Y          int
	Label      string
	TopPercent string
	Zero       bool
}

// ChartXTick is one labelled day. LeftPercent places the label under the plot;
// Align keeps the first and last labels from hanging off its edges.
type ChartXTick struct {
	X     int
	Label string
	// Sub is a second line under Label, for a week: the weekday above, the
	// day of the month below, so seven labels fit a phone's width.
	Sub         string
	LeftPercent string
	Align       string // "start", "center" or "end"
}

// BuildNetWorthChart lays out a series. currencyCode is only appended to the
// labels; minorUnit is the base currency's, to label the vertical axis.
func BuildNetWorthChart(points []NetWorthPoint, currencyCode string, minorUnit int) ChartView {
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

	x := func(i int) int {
		if len(points) == 1 {
			return chartWidth / 2
		}
		return i * chartWidth / (len(points) - 1)
	}
	view.XTicks, view.DayTicks = xAxis(points, x)

	if !found {
		return view
	}
	view.HasData = true
	view.MaxLabel = formatAmountString(maxPoint.TotalFormatted) + " " + currencyCode
	view.MinLabel = formatAmountString(minPoint.TotalFormatted) + " " + currencyCode

	bottom, top, step := yScale(minPoint.TotalMinor, maxPoint.TotalMinor, minorUnit)
	y := func(v int64) int { return scaleY(v, bottom, top) }

	view.ZeroY = y(0)
	view.HasZeroLine = bottom < 0
	wholeUnits := step%pow10(minorUnit) == 0
	for v := bottom; ; v += step {
		ty := y(v)
		view.YTicks = append(view.YTicks, ChartYTick{
			Y:          ty,
			Label:      axisLabel(v, minorUnit, wholeUnits),
			TopPercent: percent(ty, chartHeight),
			Zero:       v == 0,
		})
		if v >= top {
			break
		}
	}

	var run []int
	flush := func() {
		if len(run) == 0 {
			return
		}
		view.Segments = append(view.Segments, segmentFor(run, points, x, y, view.ZeroY))
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

// yScale chooses the vertical axis: it always contains zero, and its ends and
// step are round numbers -- a step of 1, 2 or 5 times a power of ten, in minor
// units -- so the gridlines read 0, 1,000, 2,000 rather than 0, 937.41.
//
// The ends are computed on big.Int and checked, because a total near int64's
// limit rounded up to the next step would wrap around.
func yScale(lowest, highest int64, minorUnit int) (bottom, top, step int64) {
	lo, hi := min(lowest, 0), max(highest, 0)
	if lo == hi {
		// Everything is zero: draw a band of one whole unit above it.
		hi = pow10(minorUnit)
	}

	span := new(big.Int).Sub(big.NewInt(hi), big.NewInt(lo))
	raw := new(big.Int).Div(span, big.NewInt(chartYIntervals))
	if raw.Sign() == 0 {
		raw.SetInt64(1)
	}
	niceStep := niceAbove(raw)

	topBig := ceilMultiple(big.NewInt(hi), niceStep)
	bottomBig := new(big.Int).Neg(ceilMultiple(big.NewInt(-lo), niceStep))
	if !niceStep.IsInt64() || !topBig.IsInt64() || !bottomBig.IsInt64() {
		// No room to round outwards: the data's own ends will do.
		return lo, hi, hi - lo
	}
	return bottomBig.Int64(), topBig.Int64(), niceStep.Int64()
}

// niceAbove is the smallest of 1, 2 or 5 times a power of ten that is >= v.
func niceAbove(v *big.Int) *big.Int {
	power := big.NewInt(1)
	ten := big.NewInt(10)
	for {
		for _, m := range []int64{1, 2, 5} {
			candidate := new(big.Int).Mul(power, big.NewInt(m))
			if candidate.Cmp(v) >= 0 {
				return candidate
			}
		}
		power.Mul(power, ten)
	}
}

// ceilMultiple rounds a non-negative v up to a multiple of step.
func ceilMultiple(v, step *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(v, step, new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Mul(q, step)
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// axisLabel writes an axis value, in minor units, as an amount: "2,500" when
// every gridline falls on a whole unit, "2,500.50" otherwise.
func axisLabel(v int64, minorUnit int, wholeUnits bool) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	unit := pow10(minorUnit)
	s := strconv.FormatInt(v/unit, 10)
	if !wholeUnits && minorUnit > 0 {
		frac := strconv.FormatInt(v%unit, 10)
		s += "." + strings.Repeat("0", minorUnit-len(frac)) + frac
	}
	return formatAmountString(sign + s)
}

// percent writes part/whole as a CSS percentage with two decimals, from
// integers: 120 of 240 is "50.00%".
func percent(part, whole int) string {
	hundredths := part * 10000 / whole
	frac := strconv.Itoa(hundredths % 100)
	if len(frac) == 1 {
		frac = "0" + frac
	}
	return strconv.Itoa(hundredths/100) + "." + frac + "%"
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

// segmentFor draws one run. Its area is closed down (or up) to zero, so the
// filled shape is the money itself, not the distance from an arbitrary floor.
func segmentFor(run []int, points []NetWorthPoint, x func(int) int, y func(int64) int, zeroY int) ChartSegment {
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
	base := strconv.Itoa(zeroY)
	area := line.String() + " L" + strconv.Itoa(last) + " " + base + " L" + strconv.Itoa(first) + " " + base + " Z"
	return ChartSegment{Line: line.String(), Area: area}
}

// xAxis chooses which days to label, by how long the range is, and draws a
// small mark for every day when there are few enough to tell apart:
//
//	up to 8 days    every day        "lun" over "5"
//	up to 35 days   every Monday     "5 oct"
//	up to 100 days  the 1st and 15th "15 oct"
//	up to 400 days  every 1st        "oct", with the year in January
//	longer          every quarter    "oct 26"
func xAxis(points []NetWorthPoint, x func(int) int) ([]ChartXTick, string) {
	n := len(points)
	var ticks []ChartXTick
	for i, p := range points {
		d := p.Date
		var label, sub string
		switch {
		case n <= 8:
			label, sub = spanishWeekdays[d.Weekday()], strconv.Itoa(d.Day())
		case n <= 35:
			if d.Weekday() == time.Monday {
				label = strconv.Itoa(d.Day()) + " " + spanishShortMonths[d.Month()-1]
			}
		case n <= 100:
			if d.Day() == 1 || d.Day() == 15 {
				label = strconv.Itoa(d.Day()) + " " + spanishShortMonths[d.Month()-1]
			}
		case n <= 400:
			if d.Day() == 1 {
				label = spanishShortMonths[d.Month()-1]
				if d.Month() == time.January {
					label += " " + strconv.Itoa(d.Year()%100)
				}
			}
		default:
			if d.Day() == 1 && (d.Month()-1)%3 == 0 {
				label = spanishShortMonths[d.Month()-1] + " " + strconv.Itoa(d.Year()%100)
			}
		}
		if label == "" {
			continue
		}
		tx := x(i)
		align := "center"
		switch {
		case tx < chartWidth/20:
			align = "start"
		case tx > chartWidth-chartWidth/20:
			align = "end"
		}
		ticks = append(ticks, ChartXTick{X: tx, Label: label, Sub: sub, LeftPercent: percent(tx, chartWidth), Align: align})
	}

	if n > chartDayTicksMax {
		return ticks, ""
	}
	var marks strings.Builder
	for i := range points {
		if i > 0 {
			marks.WriteByte(' ')
		}
		s := strconv.Itoa(x(i))
		marks.WriteString("M" + s + " " + strconv.Itoa(chartHeight) + " L" + s + " " + strconv.Itoa(chartHeight-6))
	}
	return ticks, marks.String()
}

var spanishShortMonths = [...]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

var spanishWeekdays = [...]string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}

// shortSpanishDate renders a calendar date as "5 oct 2026". The date is read
// as stored -- UTC midnight naming a Venezuelan calendar day -- and is NOT moved
// into the economic zone, which would turn it into the evening before.
func shortSpanishDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.Itoa(t.Day()) + " " + spanishShortMonths[t.Month()-1] + " " + strconv.Itoa(t.Year())
}
