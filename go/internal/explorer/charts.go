package explorer

// Server-side SVG charts for the /stats page. No JavaScript: the explorer's
// content-security policy allows none, and the page stays fast on a phone.
// Every string placed in the SVG goes through esc().

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"
)

const (
	cW, cH         = 760.0, 260.0 // a half-width chart
	wideW          = 1540.0       // a full-width chart: same text scale on screen
	cL, cR, cT, cB = 60.0, 18.0, 58.0, 34.0
	colAccent      = "#00d4aa"
	colAccent2     = "#ff6b9d"
	colWarn        = "#ffb454"
	colGrid        = "#1f2a3a"
	colText        = "#c8d3e0"
	colMuted       = "#6b7a8e"
)

// Distinct hues for transaction types (same type, same colour everywhere).
var typeColors = []string{"#00d4aa", "#5b9cff", "#9b7bff", "#ff6b9d", "#ff8a5c", "#ffb454", "#e8d35a", "#4fd1c5", "#a0e05a", "#c084fc"}

func esc(s string) string { return html.EscapeString(s) }

// niceMax returns an axis top >= v made of four equal, round steps, so all
// five gridlines land on clean numbers (0, 15, 30, 45, 60 rather than
// 0, 12.5, 25, 37.5, 50).
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	step := v / 4
	p := math.Pow(10, math.Floor(math.Log10(step)))
	for _, m := range []float64{1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if step <= m*p {
			return 4 * m * p
		}
	}
	return 40 * p
}

// countMax is niceMax for whole-number axes: four gridlines, whole steps.
func countMax(v float64) float64 {
	if v <= 4 {
		return 4
	}
	return math.Ceil(niceMax(v)/4) * 4
}

// human formats 1234 as 1.2k, 3400000 as 3.4M.
func human(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return trim(fmt.Sprintf("%.1f", v/1e9)) + "B"
	case a >= 1e6:
		return trim(fmt.Sprintf("%.1f", v/1e6)) + "M"
	case a >= 1e3:
		return trim(fmt.Sprintf("%.1f", v/1e3)) + "k"
	case a >= 10 || v == math.Trunc(v):
		return fmt.Sprintf("%.0f", v)
	}
	return trim(fmt.Sprintf("%.1f", v))
}

func trim(s string) string { return strings.TrimSuffix(s, ".0") }

type svgBuf struct {
	strings.Builder
	w float64
}

func (b *svgBuf) f(format string, a ...any) { fmt.Fprintf(&b.Builder, format, a...) }

// frame opens an SVG with its title and subtitle (the finding first).
func frame(b *svgBuf, w, height float64, title, subtitle string) {
	b.w = w
	b.f(`<svg viewBox="0 0 %g %g" role="img" aria-label="%s" class="chart">`, w, height, esc(title))
	b.f(`<text x="16" y="24" font-size="15" font-weight="600" fill="%s">%s</text>`, colText, esc(title))
	if subtitle != "" {
		b.f(`<text x="16" y="43" font-size="11.5" fill="%s">%s</text>`, colMuted, esc(subtitle))
	}
}

// legend draws key entries right-aligned in the header band. kind "dash"
// draws a dashed line swatch (for reference lines), otherwise a solid one.
type legendItem struct{ Label, Color, Kind string }

func legend(b *svgBuf, items []legendItem) {
	x := b.w - cR
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		x -= float64(len(it.Label))*6.4 + 30
		if it.Kind == "dash" {
			b.f(`<line x1="%.1f" x2="%.1f" y1="38" y2="38" stroke="%s" stroke-width="1.5" stroke-dasharray="4 3"/>`, x, x+16, it.Color)
		} else {
			b.f(`<rect x="%.1f" y="36" width="16" height="4" rx="1" fill="%s"/>`, x, it.Color)
		}
		b.f(`<text x="%.1f" y="42" font-size="11" fill="%s">%s</text>`, x+21, colMuted, esc(it.Label))
	}
}

// yAxis draws gridlines and labels from 0 to top.
func yAxis(b *svgBuf, top, height float64, fmtY func(float64) string) func(float64) float64 {
	plotH := height - cT - cB
	Y := func(v float64) float64 { return cT + plotH - v/top*plotH }
	for i := 0; i <= 4; i++ {
		v := top * float64(i) / 4
		y := Y(v)
		b.f(`<line x1="%g" x2="%g" y1="%.1f" y2="%.1f" stroke="%s"/>`, cL, b.w-cR, y, y, colGrid)
		b.f(`<text x="%g" y="%.1f" font-size="11" text-anchor="end" fill="%s">%s</text>`, cL-8, y+4, colMuted, esc(fmtY(v)))
	}
	return Y
}

// xLabels prints evenly spaced category labels (more on wide charts).
func xLabels(b *svgBuf, n int, X func(int) float64, label func(int) string, baseline float64) {
	want := 7.0
	if b.w > cW {
		want = 14
	}
	step := max(1, int(math.Ceil(float64(n)/want)))
	for i := 0; i < n; i += step {
		b.f(`<text x="%.1f" y="%g" font-size="11" text-anchor="middle" fill="%s">%s</text>`, X(i), baseline+18, colMuted, esc(label(i)))
	}
}

type barDatum struct {
	Label string
	Value float64
	Tip   string
}

// barChart draws one bar per datum, with an optional reference line.
func barChart(w float64, title, subtitle string, data []barDatum, fmtY func(float64) string, ref float64, refLabel string) template.HTML {
	var b svgBuf
	frame(&b, w, cH, title, subtitle)
	if len(data) == 0 {
		return emptyChart(&b)
	}
	peak := ref
	for _, d := range data {
		peak = math.Max(peak, d.Value)
	}
	top := niceMax(peak * 1.05)
	Y := yAxis(&b, top, cH, fmtY)
	band := (w - cL - cR) / float64(len(data))
	X := func(i int) float64 { return cL + band*(float64(i)+0.5) }
	bw := math.Max(1, math.Min(band*0.72, 26))
	for i, d := range data {
		y := Y(d.Value)
		b.f(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="1.5" fill="%s" fill-opacity="0.85"><title>%s</title></rect>`,
			X(i)-bw/2, y, bw, Y(0)-y, colAccent, esc(d.Tip))
	}
	if ref > 0 {
		y := Y(ref)
		b.f(`<line x1="%g" x2="%g" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5" stroke-dasharray="5 4"/>`, cL, w-cR, y, y, colWarn)
		legend(&b, []legendItem{{refLabel, colWarn, "dash"}})
	}
	xLabels(&b, len(data), X, func(i int) string { return data[i].Label }, cH-cB)
	b.f(`</svg>`)
	return template.HTML(b.String())
}

type linePoint struct {
	X, Y float64
	Tip  string
}

// lineChart draws one or more series over a numeric x axis. counts makes
// the y axis whole numbers.
func lineChart(title, subtitle string, series [][]linePoint, colors []string, names []string,
	fmtX func(float64) string, fmtY func(float64) string, counts bool) template.HTML {
	var b svgBuf
	frame(&b, cW, cH, title, subtitle)
	var pts []linePoint
	for _, s := range series {
		pts = append(pts, s...)
	}
	if len(pts) < 2 {
		return emptyChart(&b)
	}
	minX, maxX, peak := pts[0].X, pts[0].X, 0.0
	for _, p := range pts {
		minX, maxX, peak = math.Min(minX, p.X), math.Max(maxX, p.X), math.Max(peak, p.Y)
	}
	if maxX == minX {
		maxX = minX + 1
	}
	top := niceMax(peak * 1.08)
	if counts {
		top = countMax(peak)
	}
	Y := yAxis(&b, top, cH, fmtY)
	X := func(x float64) float64 { return cL + (x-minX)/(maxX-minX)*(cW-cL-cR) }
	path := func(s []linePoint) string {
		var d strings.Builder
		for i, p := range s {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&d, "%s%.1f %.1f", cmd, X(p.X), Y(p.Y))
		}
		return d.String()
	}
	for si, s := range series {
		if len(s) == 0 {
			continue
		}
		if len(series) == 1 { // a single series gets a soft area fill
			b.f(`<path d="%sL%.1f %.1fL%.1f %.1fZ" fill="%s" fill-opacity="0.10"/>`, path(s), X(s[len(s)-1].X), Y(0), X(s[0].X), Y(0), colors[si])
		}
		b.f(`<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round"/>`, path(s), colors[si])
		if len(s) <= 120 {
			for _, p := range s {
				b.f(`<circle cx="%.1f" cy="%.1f" r="2.5" fill="%s"><title>%s</title></circle>`, X(p.X), Y(p.Y), colors[si], esc(p.Tip))
			}
		}
	}
	if len(names) > 1 {
		items := make([]legendItem, len(names))
		for i, n := range names {
			items[i] = legendItem{n, colors[i], "solid"}
		}
		legend(&b, items)
	}
	prev := ""
	for i := 0; i <= 5; i++ {
		v := minX + (maxX-minX)*float64(i)/5
		if lbl := fmtX(v); lbl != prev { // short spans would repeat labels
			b.f(`<text x="%.1f" y="%g" font-size="11" text-anchor="middle" fill="%s">%s</text>`, X(v), cH-cB+18, colMuted, esc(lbl))
			prev = lbl
		}
	}
	b.f(`</svg>`)
	return template.HTML(b.String())
}

type hbarDatum struct {
	Label, Value, Tip string
	Frac              float64 // 0..1
	Accent            bool
}

// hbarChart draws labelled horizontal bars (e.g. share of blocks).
func hbarChart(title, subtitle string, data []hbarDatum) template.HTML {
	rowH := 30.0
	height := cT + rowH*float64(max(1, len(data))) + 16
	var b svgBuf
	frame(&b, cW, height, title, subtitle)
	if len(data) == 0 {
		return emptyChart(&b)
	}
	labelW, valueW := 150.0, 130.0
	barX := 16 + labelW
	barMax := cW - cR - valueW - barX
	for i, d := range data {
		y := cT + rowH*float64(i)
		c := colMuted
		if d.Accent {
			c = colAccent
		}
		b.f(`<text x="16" y="%.1f" font-size="12" fill="%s" font-family="Consolas, monospace">%s</text>`, y+16, colText, esc(d.Label))
		b.f(`<rect x="%.1f" y="%.1f" width="%.1f" height="16" rx="2" fill="%s" fill-opacity="0.18"/>`, barX, y+4, barMax, colGrid)
		b.f(`<rect x="%.1f" y="%.1f" width="%.1f" height="16" rx="2" fill="%s"><title>%s</title></rect>`, barX, y+4, math.Max(2, barMax*d.Frac), c, esc(d.Tip))
		b.f(`<text x="%.1f" y="%.1f" font-size="12" fill="%s">%s</text>`, barX+barMax+10, y+16, colText, esc(d.Value))
	}
	b.f(`</svg>`)
	return template.HTML(b.String())
}

// stackedBars draws per-window counts stacked by category with a legend.
func stackedBars(title, subtitle string, labels []string, stacks [][]int, names []string, tips []string) template.HTML {
	var b svgBuf
	frame(&b, cW, cH, title, subtitle)
	if len(stacks) == 0 {
		return emptyChart(&b)
	}
	peak := 0
	for _, s := range stacks {
		t := 0
		for _, v := range s {
			t += v
		}
		peak = max(peak, t)
	}
	Y := yAxis(&b, countMax(float64(peak)), cH, func(v float64) string { return human(v) })
	band := (cW - cL - cR) / float64(len(stacks))
	X := func(i int) float64 { return cL + band*(float64(i)+0.5) }
	bw := math.Max(1, math.Min(band*0.72, 26))
	used := map[int]bool{}
	for i, s := range stacks {
		acc := 0.0
		for k, v := range s {
			if v == 0 {
				continue
			}
			used[k] = true
			y0, y1 := Y(acc), Y(acc+float64(v))
			b.f(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"><title>%s: %d %s</title></rect>`,
				X(i)-bw/2, y1, bw, y0-y1, typeColors[k%len(typeColors)], esc(tips[i]), v, esc(names[k]))
			acc += float64(v)
		}
	}
	var items []legendItem
	for k, n := range names {
		if used[k] {
			items = append(items, legendItem{n, typeColors[k%len(typeColors)], "solid"})
		}
	}
	legend(&b, items)
	if len(used) == 0 {
		b.f(`<text x="%g" y="%g" font-size="13" text-anchor="middle" fill="%s">No transactions besides block rewards in this range yet.</text>`, cW/2, cH/2+10, colMuted)
	}
	xLabels(&b, len(stacks), X, func(i int) string { return labels[i] }, cH-cB)
	b.f(`</svg>`)
	return template.HTML(b.String())
}

// histogram256 draws byte-value counts against the uniform expectation.
func histogram256(title, subtitle string, counts [256]int, n int) template.HTML {
	w := wideW
	var b svgBuf
	frame(&b, w, cH, title, subtitle)
	if n == 0 {
		return emptyChart(&b)
	}
	exp := float64(n) / 256
	peak := exp
	for _, c := range counts {
		peak = math.Max(peak, float64(c))
	}
	Y := yAxis(&b, niceMax(peak*1.1), cH, func(v float64) string { return human(v) })
	band := (w - cL - cR) / 256
	X := func(i int) float64 { return cL + band*float64(i) }
	for i, c := range counts {
		y := Y(float64(c))
		b.f(`<rect x="%.2f" y="%.1f" width="%.2f" height="%.1f" fill="%s" fill-opacity="0.8"><title>byte %d (0x%02x): %d times, expected %.1f</title></rect>`,
			X(i), y, math.Max(0.6, band-1), Y(0)-y, colAccent, i, i, c, exp)
	}
	y := Y(exp)
	b.f(`<line x1="%g" x2="%g" y1="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5" stroke-dasharray="5 4"/>`, cL, w-cR, y, y, colWarn)
	legend(&b, []legendItem{{fmt.Sprintf("perfectly even: %.1f per value", exp), colWarn, "dash"}})
	for _, v := range []int{0, 32, 64, 96, 128, 160, 192, 224, 255} {
		b.f(`<text x="%.1f" y="%g" font-size="11" text-anchor="middle" fill="%s">%d</text>`, X(v)+band/2, cH-cB+18, colMuted, v)
	}
	b.f(`</svg>`)
	return template.HTML(b.String())
}

func emptyChart(b *svgBuf) template.HTML {
	b.f(`<text x="%g" y="%g" font-size="13" text-anchor="middle" fill="%s">Not enough data yet — check back in a few minutes.</text></svg>`, b.w/2, cH/2+10, colMuted)
	return template.HTML(b.String())
}
