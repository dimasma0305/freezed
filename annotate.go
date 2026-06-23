package main

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/charmbracelet/freeze/svg"
)

// freezed annotation layer: pinpoint the vulnerable line(s) in a code screenshot.
//
//	--mark   <selector>   numbered badge (1,2,3...) on the matched line(s)
//	--circle <selector>   hand-drawn red ring around the matched line(s)
//	--note   <text>       callout text under the figure (paired with --circle)
//
// A <selector> points at the bug by line number or by quoting it, so it survives
// edits: "29" (a line), "36,38" (a line range), "AddHandler type-map" (a substring),
// or "from..to" (a block; the end is matched first, the start is the closest match
// above it, so a generic start like "return d.ring.Del(" stays unambiguous).
const (
	annRed      = "#FF5A52"
	annInk      = "#0D1117"
	annInkSoft  = "#161B22"
	annText     = "#C9D1D9"
	annTintOpac = "0.13"
)

func ff(v float64) string { return fmt.Sprintf("%.2f", v) }

func hasAnnotations(c *Config) bool { return len(c.Mark) > 0 || len(c.Circle) > 0 }

// resolveSelector maps a selector to a visible 0-based line range [i0,i1].
func resolveSelector(visible []string, offset int, sel string) (int, int, bool) {
	absToVis := func(n int) int { return n - 1 - offset } // 1-based file line -> visible index
	in := func(i int) bool { return i >= 0 && i < len(visible) }

	if n, err := strconv.Atoi(strings.TrimSpace(sel)); err == nil {
		i := absToVis(n)
		return i, i, in(i)
	}
	if a, b, ok := strings.Cut(sel, ","); ok {
		na, ea := strconv.Atoi(strings.TrimSpace(a))
		nb, eb := strconv.Atoi(strings.TrimSpace(b))
		if ea == nil && eb == nil {
			i0, i1 := absToVis(na), absToVis(nb)
			return i0, i1, in(i0) && in(i1)
		}
	}
	if from, to, ok := strings.Cut(sel, ".."); ok {
		e := containsFrom(visible, to, 0)
		if e < 0 {
			return 0, 0, false
		}
		s := containsBack(visible, from, e)
		if s < 0 {
			return 0, 0, false
		}
		if s > e {
			s, e = e, s
		}
		return s, e, true
	}
	i := containsFrom(visible, sel, 0)
	return i, i, i >= 0
}

func containsFrom(lines []string, sub string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.Contains(lines[i], sub) {
			return i
		}
	}
	return -1
}

func containsBack(lines []string, sub string, hi int) int {
	if hi >= len(lines) {
		hi = len(lines) - 1
	}
	for i := hi; i >= 0; i-- {
		if strings.Contains(lines[i], sub) {
			return i
		}
	}
	return -1
}

// applyAnnotations draws the marks/rings onto the SVG and returns the (possibly
// widened) image dimensions. It uses freeze's own line geometry so the overlay
// lands exactly on the rendered text.
func applyAnnotations(image, terminal *etree.Element, cfg *Config, scale float64, visible []string, offset int, imgW, imgH float64) (float64, float64) {
	fs := cfg.Font.Size * scale                             // glyph size in px
	charW := cfg.Font.Size / fontHeightToWidthRatio * scale // monospace advance
	padLeft := cfg.Padding[left]

	// Read the line positions freeze actually assigned, so the overlay lands
	// exactly on the rendered text instead of trusting a recomputed formula.
	var texts []*etree.Element
	if tg := image.SelectElement("g"); tg != nil {
		texts = tg.SelectElements("text")
	}
	getf := func(e *etree.Element, attr string) float64 {
		if e == nil {
			return 0
		}
		v, _ := strconv.ParseFloat(strings.TrimSuffix(e.SelectAttrValue(attr, "0"), "px"), 64)
		return v
	}
	baseY := func(i int) float64 {
		if i < 0 || i >= len(texts) {
			return 0
		}
		return getf(texts[i], "y")
	}
	gutter := 0.0
	if cfg.ShowLineNumbers {
		gutter = 5 * charW // "%3d  "
	}
	codeLeft := padLeft + cfg.Margin[left] + gutter
	if len(texts) > 0 {
		codeLeft = getf(texts[0], "x") + gutter
	}
	rowTop := func(i int) float64 { return baseY(i) - 0.80*fs }
	rowBot := func(i int) float64 { return baseY(i) + 0.26*fs }
	contentW := func(i int) float64 {
		if i < 0 || i >= len(visible) {
			return 0
		}
		s := strings.ReplaceAll(visible[i], "\t", "    ")
		return float64(len([]rune(s))) * charW
	}

	// Size the badge to a line height so badges on adjacent rows never collide.
	rowAdvance := fs * 1.2
	if len(texts) >= 2 {
		if d := baseY(1) - baseY(0); d > 0 {
			rowAdvance = d
		}
	}
	badgeR := 0.44 * rowAdvance
	rightCol := 0.0
	if len(cfg.Mark) > 0 {
		rightCol = 2*badgeR + 48*scale // room for the bracket + connector + badge
	}
	newW := imgW + rightCol

	ann := etree.NewElement("g")
	ann.CreateAttr("id", "annotations")
	ann.CreateAttr("font-family", cfg.Font.Family)

	// tint a row range and return its box
	box := func(i0, i1 int) (l, t, r, b float64) {
		t, b = rowTop(i0), rowBot(i1)
		l = codeLeft - 6*scale
		r = codeLeft
		for i := i0; i <= i1; i++ {
			if x := codeLeft + contentW(i) + 8*scale; x > r {
				r = x
			}
		}
		maxR := imgW - padLeft - 2*scale
		if r > maxR {
			r = maxR
		}
		return
	}
	tint := func(l, t, r, b float64) {
		rect := etree.NewElement("rect")
		rect.CreateAttr("x", ff(l))
		rect.CreateAttr("y", ff(t))
		rect.CreateAttr("width", ff(r-l))
		rect.CreateAttr("height", ff(b-t))
		rect.CreateAttr("rx", ff(4*scale))
		rect.CreateAttr("fill", annRed)
		rect.CreateAttr("fill-opacity", annTintOpac)
		ann.AddChild(rect)
	}

	// --circle: tint + a hand-drawn ring. Resolve every selector, then merge
	// overlapping or adjacent ranges so two circles on neighbouring lines render
	// as one clean ring instead of overlapping outlines.
	type lineRange struct{ a, b int }
	var ranges []lineRange
	for _, sel := range cfg.Circle {
		i0, i1, ok := resolveSelector(visible, offset, sel)
		if !ok {
			continue
		}
		if i0 > i1 {
			i0, i1 = i1, i0
		}
		ranges = append(ranges, lineRange{i0, i1})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].a < ranges[j].a })
	var merged []lineRange
	for _, r := range ranges {
		if n := len(merged); n > 0 && r.a <= merged[n-1].b+1 {
			if r.b > merged[n-1].b {
				merged[n-1].b = r.b
			}
			continue
		}
		merged = append(merged, r)
	}
	for idx, r := range merged {
		l, t, rr, b := box(r.a, r.b)
		tint(l, t, rr, b)
		ann.AddChild(roughRing(l-3*scale, t-4*scale, rr+3*scale, b+4*scale, scale, int64(idx)*131+7))
	}

	// --mark: tint the row(s); a multi-row selector gets a bracket spanning the
	// block with ONE badge centered on it, a single row gets a short connector.
	line := func(x1, y1, x2, y2, w float64) {
		l := etree.NewElement("line")
		l.CreateAttr("x1", ff(x1))
		l.CreateAttr("y1", ff(y1))
		l.CreateAttr("x2", ff(x2))
		l.CreateAttr("y2", ff(y2))
		l.CreateAttr("stroke", annRed)
		l.CreateAttr("stroke-width", ff(w))
		l.CreateAttr("stroke-linecap", "round")
		ann.AddChild(l)
	}
	badgeX := newW - padLeft - badgeR - 4*scale
	bracketX := badgeX - badgeR - 18*scale
	for idx, sel := range cfg.Mark {
		i0, i1, ok := resolveSelector(visible, offset, sel)
		if !ok {
			continue
		}
		if i0 > i1 {
			i0, i1 = i1, i0
		}
		l, t, r, b := box(i0, i1)
		tint(l, t, r, b)
		midY := (rowTop(i0) + rowBot(i1)) / 2

		if i1 > i0 {
			topY := rowTop(i0) + 3*scale
			botY := rowBot(i1) - 3*scale
			line(bracketX, topY, bracketX, botY, 2.2*scale)         // vertical span
			line(bracketX, topY, bracketX-8*scale, topY, 2.2*scale) // top cap
			line(bracketX, botY, bracketX-8*scale, botY, 2.2*scale) // bottom cap
			line(bracketX, midY, badgeX-badgeR, midY, 2.2*scale)    // tick to the badge
		} else {
			line(r, midY, badgeX-badgeR, midY, 1.6*scale) // single row: from the line itself
		}

		c := etree.NewElement("circle")
		c.CreateAttr("cx", ff(badgeX))
		c.CreateAttr("cy", ff(midY))
		c.CreateAttr("r", ff(badgeR))
		c.CreateAttr("fill", annRed)
		ann.AddChild(c)

		bf := 1.18 * badgeR
		num := etree.NewElement("text")
		num.CreateAttr("x", ff(badgeX))
		// resvg/libsvg ignore dominant-baseline, so center the digit by hand.
		num.CreateAttr("y", ff(midY+0.34*bf))
		num.CreateAttr("fill", "#FFFFFF")
		num.CreateAttr("font-size", ff(bf))
		num.CreateAttr("font-weight", "700")
		num.CreateAttr("text-anchor", "middle")
		num.SetText(strconv.Itoa(idx + 1))
		ann.AddChild(num)
	}

	image.AddChild(ann)

	if rightCol > 0 {
		svg.SetDimensions(image, newW, imgH)
		if wAttr := terminal.SelectAttr("width"); wAttr != nil {
			cur, _ := strconv.ParseFloat(strings.TrimSuffix(wAttr.Value, "px"), 64)
			wAttr.Value = ff(cur + rightCol)
		}
	}
	return newW, imgH
}

// roughRing returns a hand-drawn red ring (two slightly jittered passes) around
// the given box, so it reads as marked by hand rather than a clean rectangle.
func roughRing(x0, y0, x1, y1, scale float64, seed int64) *etree.Element {
	r := 9 * scale
	pts := roundRectPath(x0, y0, x1, y1, r)
	g := etree.NewElement("g")
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec
	for pass := 0; pass < 2; pass++ {
		var sb strings.Builder
		for i, p := range pts {
			jx := (rng.Float64()*2 - 1) * 1.5 * scale
			jy := (rng.Float64()*2 - 1) * 1.5 * scale
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			sb.WriteString(fmt.Sprintf("%s%.2f %.2f ", cmd, p[0]+jx, p[1]+jy))
		}
		path := etree.NewElement("path")
		path.CreateAttr("d", sb.String())
		path.CreateAttr("fill", "none")
		path.CreateAttr("stroke", annRed)
		path.CreateAttr("stroke-width", ff(3*scale))
		path.CreateAttr("stroke-linecap", "round")
		path.CreateAttr("stroke-linejoin", "round")
		g.AddChild(path)
	}
	return g
}

// roundRectPath samples the perimeter of a rounded rectangle (clockwise) into
// points, with a couple of midpoints per side so the jittered stroke wobbles.
func roundRectPath(x0, y0, x1, y1, r float64) [][2]float64 {
	var p [][2]float64
	arc := func(cx, cy, a0, a1 float64) {
		for a := a0; a <= a1+0.001; a += 18 {
			rad := a * math.Pi / 180
			p = append(p, [2]float64{cx + r*math.Cos(rad), cy + r*math.Sin(rad)})
		}
	}
	seg := func(ax, ay, bx, by float64) {
		for k := 1; k <= 2; k++ {
			t := float64(k) / 3
			p = append(p, [2]float64{ax + (bx-ax)*t, ay + (by-ay)*t})
		}
	}
	arc(x0+r, y0+r, 180, 270)
	seg(x0+r, y0, x1-r, y0)
	arc(x1-r, y0+r, 270, 360)
	seg(x1, y0+r, x1, y1-r)
	arc(x1-r, y1-r, 0, 90)
	seg(x1-r, y1, x0+r, y1)
	arc(x0+r, y1-r, 90, 180)
	seg(x0, y1-r, x0, y0+r)
	if len(p) > 0 {
		p = append(p, p[0])
	}
	return p
}
