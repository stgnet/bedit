// Package render turns a model.Document into HTML for the live view.
//
// The goal is visual fidelity with Google Docs, not semantic purity, so
// most formatting is explicit inline CSS in points, the same unit the
// document stores. Named styles become one CSS class each, so a paragraph
// only carries inline styles for what differs from its style, mirroring how
// the document model itself is stored.
//
// Everything that came from the document is untrusted (a document can be
// imported, and later edited, by anyone the owner lets in), so all text is
// HTML-escaped, and every value that lands in a style attribute or URL is
// either a number we format ourselves or checked against a strict pattern.
package render

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/stgnet/bedit/internal/model"
)

// Page is a rendered document, ready to drop into the page template.
type Page struct {
	CSS   template.CSS  // style rules for page geometry and named styles
	Fonts []string      // stylesheet URLs for web fonts the document uses
	Body  template.HTML // the document content
}

// Render renders doc. base is the URL path that relative asset paths in
// the document (img/...) are resolved against, e.g. "/bedit/my-doc/".
func Render(doc *model.Document, base string) *Page {
	r := &renderer{doc: doc, base: base, counters: map[string][]int{}, fonts: map[string]bool{}}
	var sb strings.Builder
	r.blocks(&sb, doc.Body.Content, r.columnWidth())
	return &Page{CSS: template.CSS(r.css()), Fonts: r.fontLinks(), Body: template.HTML(sb.String())}
}

type renderer struct {
	doc      *model.Document
	base     string
	counters map[string][]int // list id -> current counter per level (0 = not started)
	fonts    map[string]bool  // font families seen
}

func (r *renderer) columnWidth() float64 {
	p := r.doc.Page
	return p.Width - p.MarginLeft - p.MarginRight
}

// pt formats a length in points, trimming needless decimals.
func pt(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) + "pt"
}

// ---------------------------------------------------------------------------
// CSS

func (r *renderer) css() string {
	var sb strings.Builder
	p := r.doc.Page
	// The page: a white sheet as wide as the original's paper, with its
	// side margins as padding, so lines break where they did in Google
	// Docs. On screens narrower than that, fall back to the full width
	// with small margins rather than forcing sideways scrolling.
	fmt.Fprintf(&sb, ".page{width:%s;padding:%s %s %s %s}\n",
		pt(p.Width-p.MarginLeft-p.MarginRight), pt(p.MarginTop), pt(p.MarginRight), pt(p.MarginBottom), pt(p.MarginLeft))
	fmt.Fprintf(&sb, "@media (max-width:%dpx){.page{width:auto;padding:24px 16px}}\n", int(p.Width*4/3)+48)

	keys := make([]string, 0, len(r.doc.Styles))
	for k := range r.doc.Styles {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable output, easier to diff
	for _, k := range keys {
		st := r.doc.Styles[k]
		if !classRE.MatchString(k) {
			continue
		}
		var decl []string
		decl = append(decl, r.runCSS(st.Run)...)
		decl = append(decl, paraCSS(st.Para)...)
		fmt.Fprintf(&sb, ".page .s-%s{%s}\n", k, strings.Join(decl, ";"))
	}
	return sb.String()
}

var classRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

func (r *renderer) runCSS(rp model.RunProps) []string {
	weight, style, deco := "400", "normal", "none"
	if rp.Bold {
		weight = "700"
	}
	if rp.Italic {
		style = "italic"
	}
	switch {
	case rp.Underline && rp.Strike:
		deco = "underline line-through"
	case rp.Underline:
		deco = "underline"
	case rp.Strike:
		deco = "line-through"
	}
	color := rp.Color
	if !colorRE.MatchString(color) {
		color = "#000000"
	}
	return []string{
		"font-family:" + r.fontFamily(rp.FontFamily),
		"font-size:" + pt(rp.FontSize),
		"font-weight:" + weight,
		"font-style:" + style,
		"text-decoration:" + deco,
		"color:" + color,
	}
}

func paraCSS(pp model.ParaProps) []string {
	d := []string{
		"text-align:" + align(pp.Align),
		// Paragraph spacing is padding, not margin: CSS collapses
		// adjacent vertical margins (6pt after + 10pt before = 10pt),
		// while Docs adds them (16pt). Padding never collapses.
		"padding-top:" + pt(pp.SpaceBefore),
		"padding-bottom:" + pt(pp.SpaceAfter),
		"margin-left:" + pt(pp.IndentLeft),
		"margin-right:" + pt(pp.IndentRight),
		"text-indent:" + pt(pp.IndentFirst),
	}
	return append(d, lineHeight(pp.LineHeight, pp.LineExact)...)
}

func lineHeight(mult, exact float64) []string {
	if exact > 0 {
		return []string{"line-height:" + pt(exact)}
	}
	if mult > 0 {
		return []string{"line-height:" + strconv.FormatFloat(mult, 'f', -1, 64)}
	}
	return nil
}

func align(a string) string {
	switch a {
	case "center", "right", "justify":
		return a
	}
	return "left"
}

var colorRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// fontRE is what a font family name may contain before it is put in CSS.
// Real family names fit easily; anything else could break out of the
// declaration, so it is dropped.
var fontRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 \-]{0,60}$`)

// Generic fallbacks for well-known families, so text keeps roughly the
// right look while a web font loads, or if it can't be loaded at all.
var serifFonts = map[string]bool{
	"Times New Roman": true, "Georgia": true, "Cambria": true, "Merriweather": true, "Lora": true,
	"Playfair Display": true, "EB Garamond": true, "Garamond": true, "PT Serif": true,
	"Roboto Slab": true, "Source Serif Pro": true, "Libre Baskerville": true, "Crimson Text": true,
	"Spectral": true, "Alegreya": true, "Domine": true,
}
var monoFonts = map[string]bool{
	"Courier New": true, "Roboto Mono": true, "Source Code Pro": true, "Inconsolata": true,
	"Consolas": true, "Courier": true, "Ubuntu Mono": true, "Space Mono": true,
}

func (r *renderer) fontFamily(name string) string {
	generic := "sans-serif"
	if serifFonts[name] {
		generic = "serif"
	} else if monoFonts[name] {
		generic = "monospace"
	}
	if !fontRE.MatchString(name) {
		return generic
	}
	r.fonts[name] = true
	return "'" + name + "'," + generic
}

// systemFonts are families Google Docs offers that are not served by Google
// Fonts; browsers find them (or a metric-compatible stand-in) locally.
var systemFonts = map[string]bool{
	"Arial": true, "Times New Roman": true, "Courier New": true, "Georgia": true, "Verdana": true,
	"Trebuchet MS": true, "Comic Sans MS": true, "Impact": true, "Tahoma": true, "Calibri": true,
	"Cambria": true, "Helvetica": true, "Garamond": true, "Consolas": true, "Courier": true,
}

// fontLinks returns Google Fonts stylesheet URLs for the non-system fonts
// used. Google Docs' own font menu is (mostly) Google Fonts, so this gets
// the real typeface in nearly every case.
//
// Each family gets two links: a plain one that always succeeds (regular
// weight only), and one asking for bold and italic too. The css2 API rejects
// a request outright if a family lacks any requested style (Oswald has no
// italic, Pacifico has no bold), so asking for everything in a single link
// could lose the font entirely. With two, the worst case is the browser
// synthesizing bold or italic.
func (r *renderer) fontLinks() []string {
	var names []string
	for f := range r.fonts {
		if !systemFonts[f] {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	var out []string
	for _, f := range names {
		fam := url.QueryEscape(f) // spaces become "+", as the API expects
		out = append(out,
			"https://fonts.googleapis.com/css2?family="+fam+"&display=swap",
			"https://fonts.googleapis.com/css2?family="+fam+":ital,wght@0,400;0,700;1,400;1,700&display=swap")
	}
	return out
}

// ---------------------------------------------------------------------------
// Blocks

// blocks renders block nodes. width is the width available to them (the
// page's text column, or a table cell), used to place wrapped images.
func (r *renderer) blocks(sb *strings.Builder, nodes []*model.Node, width float64) {
	for _, n := range nodes {
		switch n.Type {
		case "paragraph":
			r.paragraph(sb, n, width)
		case "table":
			r.table(sb, n)
		}
		sb.WriteByte('\n')
	}
}

// headingTags gives named styles their semantic HTML element, which helps
// screen readers and search engines; the look still comes from the style.
var headingTags = map[string]string{
	"title": "h1", "heading1": "h1", "heading2": "h2", "heading3": "h3",
	"heading4": "h4", "heading5": "h5", "heading6": "h6",
}

func (r *renderer) paragraph(sb *strings.Builder, n *model.Node, width float64) {
	styleKey := n.Str("style")
	st := r.doc.Styles[styleKey]
	if st == nil {
		st = &model.Style{}
	}
	tag := headingTags[styleKey]
	if tag == "" {
		tag = "p"
	}

	// Inline styles for whatever differs from the named style.
	var css []string
	if v := n.Str("align"); v != "" {
		css = append(css, "text-align:"+align(v))
	}
	for _, a := range []struct{ attr, prop string }{
		{"spaceBefore", "padding-top"}, {"spaceAfter", "padding-bottom"},
		{"indentLeft", "margin-left"}, {"indentRight", "margin-right"}, {"indentFirst", "text-indent"},
	} {
		if v, ok := n.Num(a.attr); ok {
			css = append(css, a.prop+":"+pt(v))
		}
	}
	lh, okH := n.Num("lineHeight")
	le, okE := n.Num("lineExact")
	if okH || okE {
		if !okH {
			lh = st.Para.LineHeight
		}
		if !okE {
			le = st.Para.LineExact
		}
		css = append(css, lineHeight(lh, le)...)
	}

	class := "s-" + styleKey
	if !classRE.MatchString(styleKey) {
		class = ""
	}
	if n.Bool("pageBreakBefore") {
		class += " pbb"
	}
	sb.WriteString("<" + tag + ` class="` + class + `"`)
	anchors, _ := n.Attrs["anchors"].([]any)
	if len(anchors) > 0 {
		if id, ok := anchors[0].(string); ok {
			sb.WriteString(` id="` + html.EscapeString(id) + `"`)
		}
	}
	if len(css) > 0 {
		sb.WriteString(` style="` + strings.Join(css, ";") + `"`)
	}
	sb.WriteByte('>')
	// Extra bookmarks on the same paragraph still need to be link targets.
	for _, a := range anchors[min(1, len(anchors)):] {
		if id, ok := a.(string); ok {
			sb.WriteString(`<a id="` + html.EscapeString(id) + `"></a>`)
		}
	}

	// Wrapped images first. A CSS float only sits beside the lines that
	// come after it, and Docs anchors these images to the paragraph as a
	// whole, so starting the paragraph with them is the closest match.
	indentL, indentR := st.Para.IndentLeft, st.Para.IndentRight
	if v, ok := n.Num("indentLeft"); ok {
		indentL = v
	}
	if v, ok := n.Num("indentRight"); ok {
		indentR = v
	}
	for _, c := range n.Content {
		if c.Type == "image" && c.Str("wrap") != "inline" {
			r.image(sb, c, width-indentL-indentR, indentL)
		}
	}

	content := n.Content
	visible := false // whether the paragraph shows anything that takes up a line
	if id := n.Str("listId"); id != "" {
		// The marker and the first word go in one no-wrap span. On
		// their own, a browser may leave the number alone on a line
		// squeezed beside an image and start the text below it; glued
		// together, they always move as one.
		sb.WriteString(`<span class="lmw">`)
		r.listMarker(sb, n, id)
		if len(content) > 0 && content[0].Type == "text" {
			first := content[0]
			word, rest := first.Text, ""
			if i := strings.IndexAny(word, " \t"); i >= 0 {
				word, rest = word[:i], word[i:]
			}
			r.text(sb, &model.Node{Type: "text", Text: word, Marks: first.Marks})
			visible = true
			content = content[1:]
			if rest != "" {
				content = append([]*model.Node{{Type: "text", Text: rest, Marks: first.Marks}}, content...)
			}
		}
		sb.WriteString(`</span>`)
	}

	for _, c := range content {
		switch c.Type {
		case "text":
			r.text(sb, c)
			visible = true
		case "hardBreak":
			sb.WriteString("<br>")
			visible = true
		case "pageBreak":
			sb.WriteString(`<span class="pb"></span>`)
			visible = true
		case "image":
			if c.Str("wrap") == "inline" {
				r.image(sb, c, width, 0)
				visible = true
			}
		}
	}
	// An empty paragraph still occupies a line in Docs; a <br> gives the
	// HTML element the same height.
	if !visible {
		sb.WriteString("<br>")
	}
	sb.WriteString("</" + tag + ">")
}

// listMarker writes the bullet or number for a list item, advancing the
// list's counters. Counting happens here, at render time, because the
// numbers depend on every earlier item in the list.
func (r *renderer) listMarker(sb *strings.Builder, n *model.Node, id string) {
	list := r.doc.Lists[id]
	lvlF, _ := n.Num("listLevel")
	lvl := int(lvlF)
	if list == nil || lvl < 0 || lvl >= len(list.Levels) {
		return
	}
	c := r.counters[id]
	if c == nil {
		c = make([]int, len(list.Levels))
		r.counters[id] = c
	}
	if c[lvl] == 0 {
		c[lvl] = list.Levels[lvl].Start
	} else {
		c[lvl]++
	}
	// A shallower item restarts the numbering of the levels below it
	// (1. a. b. 2. a. ...).
	for i := lvl + 1; i < len(c); i++ {
		c[i] = 0
	}

	L := list.Levels[lvl]
	text := L.Text
	if L.Format != "bullet" {
		// Replace %1..%9 with each level's counter in that level's
		// format; levels not started yet show their start value.
		for i := len(list.Levels); i >= 1; i-- {
			ph := "%" + strconv.Itoa(i)
			if !strings.Contains(text, ph) {
				continue
			}
			v := c[i-1]
			if v == 0 {
				v = list.Levels[i-1].Start
			}
			text = strings.ReplaceAll(text, ph, formatCounter(v, list.Levels[i-1].Format))
		}
	}
	if L.Format == "none" {
		text = ""
	}

	// The marker sits in the hanging indent: the paragraph's negative
	// text-indent pulls the first line left, and this inline-block fills
	// exactly that space, so the text lines up with the wrapped lines.
	hang := L.IndentHanging
	if v, ok := n.Num("indentFirst"); ok {
		hang = -v
	}
	css := "min-width:" + pt(max(hang, 0))
	if L.FontFamily != "" {
		css += ";font-family:" + r.fontFamily(L.FontFamily)
	}
	sb.WriteString(`<span class="lm" style="` + css + `">` + html.EscapeString(text) + `</span>`)
}

func formatCounter(v int, format string) string {
	switch format {
	case "lowerLetter":
		return letters(v, 'a')
	case "upperLetter":
		return letters(v, 'A')
	case "lowerRoman":
		return strings.ToLower(roman(v))
	case "upperRoman":
		return roman(v)
	case "decimalZero":
		return fmt.Sprintf("%02d", v)
	}
	return strconv.Itoa(v)
}

// letters writes 1..26 as a..z, then aa, bb, ... as Docs and Word do.
func letters(v int, base rune) string {
	if v < 1 {
		return ""
	}
	ch := string(base + rune((v-1)%26))
	return strings.Repeat(ch, (v-1)/26+1)
}

func roman(v int) string {
	if v < 1 || v > 3999 {
		return strconv.Itoa(v)
	}
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}
	var sb strings.Builder
	for i, n := range vals {
		for v >= n {
			sb.WriteString(syms[i])
			v -= n
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Inline content

func (r *renderer) text(sb *strings.Builder, n *model.Node) {
	var css []string
	var deco []string
	href := ""
	for _, m := range n.Marks {
		switch m.Type {
		case "bold":
			css = append(css, "font-weight:700")
		case "italic":
			css = append(css, "font-style:italic")
		case "underline":
			deco = append(deco, "underline")
		case "strike":
			deco = append(deco, "line-through")
		case "superscript":
			css = append(css, "vertical-align:super;font-size:smaller")
		case "subscript":
			css = append(css, "vertical-align:sub;font-size:smaller")
		case "highlight":
			if c, _ := m.Attrs["color"].(string); colorRE.MatchString(c) {
				css = append(css, "background-color:"+c)
			}
		case "textStyle":
			if f, _ := m.Attrs["fontFamily"].(string); f != "" {
				css = append(css, "font-family:"+r.fontFamily(f))
			}
			if s, ok := model.Number(m.Attrs["fontSize"]); ok && s > 0 {
				css = append(css, "font-size:"+pt(s))
			}
			if c, _ := m.Attrs["color"].(string); colorRE.MatchString(c) {
				css = append(css, "color:"+c)
			}
			if m.Attrs["fontWeight"] == "normal" {
				css = append(css, "font-weight:400")
			}
			if m.Attrs["fontStyle"] == "normal" {
				css = append(css, "font-style:normal")
			}
			if m.Attrs["textDecoration"] == "none" {
				css = append(css, "text-decoration:none")
			}
		case "link":
			href, _ = m.Attrs["href"].(string)
		}
	}
	if len(deco) > 0 {
		css = append(css, "text-decoration:"+strings.Join(deco, " "))
	}
	href = safeHref(href)
	if href != "" {
		sb.WriteString(`<a href="` + html.EscapeString(href) + `">`)
	}
	if len(css) > 0 {
		sb.WriteString(`<span style="` + strings.Join(css, ";") + `">`)
	}
	sb.WriteString(html.EscapeString(n.Text))
	if len(css) > 0 {
		sb.WriteString("</span>")
	}
	if href != "" {
		sb.WriteString("</a>")
	}
}

// safeHref allows only link schemes that can't run script.
func safeHref(h string) string {
	l := strings.ToLower(strings.TrimSpace(h))
	for _, p := range []string{"http://", "https://", "mailto:", "tel:", "#"} {
		if strings.HasPrefix(l, p) {
			return strings.TrimSpace(h)
		}
	}
	return ""
}

// srcRE limits image sources to the document's own img/ directory.
var srcRE = regexp.MustCompile(`^img/[a-f0-9]{8,64}\.(png|jpg|gif|webp|bmp)$`)

// image renders an image. avail is the width of the space the image sits
// in and indent is how far that space is from the left of the text column,
// both used to turn the image's column-relative position into float
// margins.
func (r *renderer) image(sb *strings.Builder, n *model.Node, avail, indent float64) {
	src := n.Str("src")
	if !srcRE.MatchString(src) {
		return
	}
	w, _ := n.Num("width")
	h, _ := n.Num("height")
	if w <= 0 || h <= 0 {
		return
	}
	dist := func(k string) float64 { v, _ := n.Num(k); return v }

	// The wrapper has the image's displayed size; aspect-ratio plus
	// max-width lets it shrink proportionally on narrow screens.
	css := []string{"width:" + pt(w), fmt.Sprintf("aspect-ratio:%g/%g", w, h)}
	class := "img"
	x, _ := n.Num("offsetX")
	x -= indent
	switch n.Str("wrap") {
	case "inline":
		// sits on the text baseline like a large character
	case "square", "tight", "through":
		// Text flows around it. Docs keeps distLeft/distRight between
		// image and text; the offset from the near edge of the column
		// becomes the margin on that side.
		side := "left"
		if n.Str("side") == "right" {
			side = "right"
		}
		// Docs places the image offsetY below the top of its paragraph,
		// with full-width text above it. A float can't be pushed down
		// by a margin without also narrowing the lines beside that
		// margin, so instead a zero-width float of that height goes
		// first and the image clears it: the spacer takes no room from
		// the text, and the image lands below it.
		if y, _ := n.Num("offsetY"); y > 0 {
			fmt.Fprintf(sb, `<span class="fsp" style="float:%s;height:%s"></span>`, side, pt(y))
			css = append(css, "clear:"+side)
		}
		//
		// When the gap left beside the image is too narrow for useful
		// text (under an inch), Docs leaves it empty; a browser would
		// squeeze a word or a list number in there. So the margin on
		// the open side is widened to cover the gap, and for right
		// floats also the paragraph's left indent, where list numbers
		// hang.
		const minGap = 72.0
		if side == "right" {
			right, left := max(avail-x-w, 0), dist("distLeft")
			if avail-w-right-left < minGap {
				left = avail - w - right + indent
			}
			css = append(css, "float:right",
				"margin:0 "+pt(right)+" "+pt(dist("distBottom"))+" "+pt(left))
		} else {
			left, right := max(x, 0), dist("distRight")
			if avail-w-left-right < minGap {
				right = max(avail-w-left, 0)
			}
			css = append(css, "float:left",
				"margin:0 "+pt(right)+" "+pt(dist("distBottom"))+" "+pt(left))
		}
	default:
		// "topBottom" (Docs' "break text"): the image has its own band
		// with no text beside it. "none" (behind / in front of text)
		// can't be layered over flowing text reliably in HTML, so it is
		// shown the same way rather than risk hiding words under it.
		class += " blk"
		css = append(css, "margin:"+pt(dist("distTop"))+" 0 "+pt(dist("distBottom"))+" "+pt(max(x, 0)))
	}

	// Cropping. Docs stores the full image plus how much to trim from
	// each edge. We show the full image scaled up inside a wrapper of
	// the cropped size, with overflow hidden. Percentages (of the
	// wrapper) keep the crop exact when the wrapper shrinks.
	inner := "width:100%;height:100%"
	if crop, ok := n.Attrs["crop"].(map[string]any); ok {
		c := func(k string) float64 { v, _ := model.Number(crop[k]); return v }
		l, t, rr, b := c("left"), c("top"), c("right"), c("bottom")
		if sw, sh := 1-l-rr, 1-t-b; sw > 0 && sh > 0 {
			inner = fmt.Sprintf("width:%.4f%%;height:%.4f%%;left:%.4f%%;top:%.4f%%",
				100/sw, 100/sh, -100*l/sw, -100*t/sh)
		}
	}

	link := safeHref(n.Str("href"))
	if link != "" {
		sb.WriteString(`<a href="` + html.EscapeString(link) + `">`)
	}
	alt := n.Str("alt")
	if alt == "" {
		alt = n.Str("title")
	}
	fmt.Fprintf(sb, `<span class="%s" style="%s"><img src="%s" alt="%s" style="%s" loading="lazy"></span>`,
		class, strings.Join(css, ";"), html.EscapeString(r.base+src), html.EscapeString(alt), inner)
	if link != "" {
		sb.WriteString("</a>")
	}
}

// ---------------------------------------------------------------------------
// Tables

func (r *renderer) table(sb *strings.Builder, n *model.Node) {
	var cols []float64
	if cw, ok := n.Attrs["colWidths"].([]any); ok {
		for _, v := range cw {
			f, _ := model.Number(v)
			cols = append(cols, f)
		}
	}
	pad := "5pt"
	if p, ok := n.Attrs["cellPadding"].([]any); ok && len(p) == 4 {
		var parts []string
		for _, v := range p {
			f, _ := model.Number(v)
			parts = append(parts, pt(f))
		}
		pad = strings.Join(parts, " ")
	}

	total := 0.0
	for _, c := range cols {
		total += c
	}
	sb.WriteString(`<table class="t"`)
	if total > 0 {
		sb.WriteString(` style="width:` + pt(total) + `"`)
	}
	sb.WriteString(">")
	if len(cols) > 0 {
		sb.WriteString("<colgroup>")
		for _, c := range cols {
			sb.WriteString(`<col style="width:` + pt(c) + `">`)
		}
		sb.WriteString("</colgroup>")
	}
	for _, row := range n.Content {
		sb.WriteString("<tr>")
		rowH, _ := row.Num("height")
		col := 0
		for _, cell := range row.Content {
			span := 1
			if v, ok := cell.Num("colspan"); ok && v > 1 {
				span = int(v)
			}
			css := []string{"padding:" + pad}
			if rowH > 0 {
				css = append(css, "height:"+pt(rowH))
			}
			if bg := cell.Str("background"); colorRE.MatchString(bg) {
				css = append(css, "background-color:"+bg)
			}
			switch cell.Str("verticalAlign") {
			case "center":
				css = append(css, "vertical-align:middle")
			case "bottom":
				css = append(css, "vertical-align:bottom")
			}
			for _, b := range []struct{ attr, prop string }{
				{"borderTop", "border-top"}, {"borderRight", "border-right"},
				{"borderBottom", "border-bottom"}, {"borderLeft", "border-left"},
			} {
				if v := cell.Str(b.attr); borderRE.MatchString(v) {
					css = append(css, b.prop+":"+v)
				}
			}
			sb.WriteString("<td")
			if span > 1 {
				fmt.Fprintf(sb, ` colspan="%d"`, span)
			}
			if v, ok := cell.Num("rowspan"); ok && v > 1 {
				fmt.Fprintf(sb, ` rowspan="%d"`, int(v))
			}
			sb.WriteString(` style="` + strings.Join(css, ";") + `">`)
			// Width available to the cell's content, for wrapped images.
			w := 0.0
			for i := col; i < col+span && i < len(cols); i++ {
				w += cols[i]
			}
			r.blocks(sb, cell.Content, w)
			sb.WriteString("</td>")
			col += span
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</table>")
}

var borderRE = regexp.MustCompile(`^(none|[0-9.]+pt solid #[0-9a-f]{6})$`)
