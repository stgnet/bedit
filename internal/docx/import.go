// Package docx converts a .docx file into bedit's document model.
//
// Why .docx is the import path for Google Docs: Google's "Download as .docx"
// export (export?format=docx) is the only anonymous export that keeps
// everything bedit cares about in one file: named styles, fonts, list
// definitions, the images themselves, and, crucially, how each image is
// anchored and how text wraps around it. The HTML export flattens styles
// into generated CSS classes and turns wrapped images into inline ones; the
// Docs API JSON would need OAuth credentials. As a bonus, Word documents
// import through the same code.
//
// The importer only reads the parts of WordprocessingML that Google
// actually emits (checked against real exports); unknown elements are
// skipped rather than rejected, so a richer file degrades gracefully.
package docx

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/stgnet/bedit/internal/model"
)

// Result is what an import produces: the document, plus the image files it
// references, keyed by the file name the document uses under img/.
type Result struct {
	Doc    *model.Document
	Images map[string][]byte
}

// Unit conversions. OOXML measures most lengths in twentieths of a point
// ("twips"), font sizes in half points, and drawing sizes in EMUs.
const (
	twipsPerPt = 20.0
	emuPerPt   = 12700.0
)

// imageTypes lists the image formats bedit will store and serve. Anything
// else (EMF/WMF vector drawings, SVG which can carry script) is dropped:
// browsers can't show the former, and the latter is unsafe to serve from
// the same origin as the editor.
var imageTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}

type rel struct {
	target   string
	external bool
}

type importer struct {
	files map[string]*zip.File
	rels  map[string]rel // relationship id -> target, for word/document.xml

	styleEls     map[string]*elem // styleId -> <w:style>
	defaultStyle string           // styleId of the default paragraph style
	defaultsPPr  *elem            // docDefaults paragraph properties
	defaultsRPr  *elem            // docDefaults run properties
	resolved     map[string]*model.Style

	nums     map[string]*elem // numId -> <w:num>
	abstract map[string]*elem // abstractNumId -> <w:abstractNum>

	doc     *model.Document
	images  map[string][]byte
	imgName map[string]string // relationship id -> stored file name

	// Field state while walking a paragraph's runs. Fields (page numbers,
	// TOC entries...) look like: begin, instruction runs, separate, result
	// runs, end. We show the cached result and skip the instructions.
	fieldDepth int
	inInstr    bool
}

// Import parses a .docx file. title is used as the document title when
// non-empty (for Google Docs the export's file name is exactly the doc
// title); otherwise the title comes from the file's properties or its first
// heading.
func Import(data []byte, title string) (*Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a .docx file: %w", err)
	}
	im := &importer{
		files:    map[string]*zip.File{},
		rels:     map[string]rel{},
		styleEls: map[string]*elem{},
		resolved: map[string]*model.Style{},
		nums:     map[string]*elem{},
		abstract: map[string]*elem{},
		images:   map[string][]byte{},
		imgName:  map[string]string{},
	}
	for _, f := range zr.File {
		im.files[f.Name] = f
	}

	body, err := im.xml("word/document.xml")
	if err != nil {
		return nil, fmt.Errorf("reading document.xml: %w", err)
	}
	body = body.child("w:body")
	if body == nil {
		return nil, fmt.Errorf("document.xml has no body")
	}
	// The relationships, styles and numbering parts are optional in
	// principle, so a missing one just leaves its map empty.
	if r, err := im.xml("word/_rels/document.xml.rels"); err == nil {
		for _, e := range r.children("rel:Relationship") {
			im.rels[e.attrv("Id")] = rel{target: e.attrv("Target"), external: e.attrv("TargetMode") == "External"}
		}
	}
	if s, err := im.xml("word/styles.xml"); err == nil {
		im.loadStyles(s)
	}
	if n, err := im.xml("word/numbering.xml"); err == nil {
		for _, e := range n.children("w:abstractNum") {
			im.abstract[e.attrv("w:abstractNumId")] = e
		}
		for _, e := range n.children("w:num") {
			im.nums[e.attrv("w:numId")] = e
		}
	}

	im.doc = &model.Document{
		Format: model.FormatVersion,
		Page:   pageSetup(body.child("w:sectPr")),
		Styles: map[string]*model.Style{},
		Lists:  map[string]*model.List{},
	}
	// Always include the default style: the renderer uses it for the base
	// font of the whole page.
	im.style("")

	im.doc.Body = &model.Node{Type: "doc", Content: im.blocks(body)}

	im.doc.Title = strings.TrimSpace(title)
	if im.doc.Title == "" {
		im.doc.Title = im.coreTitle()
	}
	if im.doc.Title == "" {
		im.doc.Title = firstHeading(im.doc.Body)
	}
	if im.doc.Title == "" {
		im.doc.Title = "Untitled document"
	}
	return &Result{Doc: im.doc, Images: im.images}, nil
}

func (im *importer) read(name string) ([]byte, error) {
	f, ok := im.files[name]
	if !ok {
		return nil, fmt.Errorf("%s not found in .docx", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (im *importer) xml(name string) (*elem, error) {
	data, err := im.read(name)
	if err != nil {
		return nil, err
	}
	return parseXML(data)
}

func (im *importer) coreTitle() string {
	core, err := im.xml("docProps/core.xml")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(core.child("dc:title").text)
}

// ---------------------------------------------------------------------------
// Page setup

func pageSetup(sect *elem) model.Page {
	// US Letter with 1" margins, Google Docs' default, for anything missing.
	p := model.Page{Width: 612, Height: 792, MarginTop: 72, MarginBottom: 72, MarginLeft: 72, MarginRight: 72}
	if sz := sect.child("w:pgSz"); sz != nil {
		setTwips(&p.Width, sz.attrv("w:w"))
		setTwips(&p.Height, sz.attrv("w:h"))
	}
	if m := sect.child("w:pgMar"); m != nil {
		setTwips(&p.MarginTop, m.attrv("w:top"))
		setTwips(&p.MarginBottom, m.attrv("w:bottom"))
		setTwips(&p.MarginLeft, m.attrv("w:left"))
		setTwips(&p.MarginRight, m.attrv("w:right"))
	}
	return p
}

// ---------------------------------------------------------------------------
// Number parsing. Google writes some measurements as "100.0", so everything
// goes through ParseFloat rather than Atoi.

func num(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func setTwips(dst *float64, s string) {
	if f, ok := num(s); ok {
		*dst = f / twipsPerPt
	}
}

func emu(s string) float64 {
	f, _ := num(s)
	return round2(f / emuPerPt)
}

// round2 keeps stored values to 1/100 pt: far below what anyone can see,
// and it keeps doc.json readable instead of full of 17-digit floats.
func round2(f float64) float64 {
	return float64(int64(f*100+copysign(0.5, f))) / 100
}

func copysign(v, sign float64) float64 {
	if sign < 0 {
		return -v
	}
	return v
}

// ---------------------------------------------------------------------------
// Styles

func (im *importer) loadStyles(s *elem) {
	dd := s.child("w:docDefaults")
	im.defaultsPPr = dd.child("w:pPrDefault").child("w:pPr")
	im.defaultsRPr = dd.child("w:rPrDefault").child("w:rPr")
	for _, st := range s.children("w:style") {
		id := st.attrv("w:styleId")
		im.styleEls[id] = st
		if st.attrv("w:type") == "paragraph" && st.attrv("w:default") == "1" {
			im.defaultStyle = id
		}
	}
}

// styleKey is the name a style is stored under in doc.json: the styleId in
// lower case ("Heading1" -> "heading1"), so keys are predictable for CSS
// class names and for the editor later.
func styleKey(id string) string {
	return strings.ToLower(id)
}

// style returns the fully resolved paragraph style for a styleId (the
// default style for ""), registering it in the document's style table.
func (im *importer) style(id string) (string, *model.Style) {
	if id == "" || im.styleEls[id] == nil {
		id = im.defaultStyle
	}
	if id == "" {
		id = "Normal"
	}
	key := styleKey(id)
	if st, ok := im.resolved[key]; ok {
		return key, st
	}

	// Walk the basedOn chain up to the root, then apply each style's
	// properties from the root down, on top of the document defaults.
	// The depth limit guards against a (malformed) basedOn cycle.
	var chain []*elem
	for cur, n := im.styleEls[id], 0; cur != nil && n < 20; n++ {
		chain = append(chain, cur)
		cur = im.styleEls[cur.val("w:basedOn")]
	}
	st := &model.Style{
		Name: id,
		Para: model.ParaProps{Align: "left", LineHeight: 1},
		Run:  model.RunProps{FontFamily: "Arial", FontSize: 11},
	}
	applyPPr(&st.Para, im.defaultsPPr)
	applyRPr(&st.Run, im.defaultsRPr)
	for i := len(chain) - 1; i >= 0; i-- {
		applyPPr(&st.Para, chain[i].child("w:pPr"))
		applyRPr(&st.Run, chain[i].child("w:rPr"))
	}
	if len(chain) > 0 {
		st.Name = displayStyleName(chain[0].val("w:name"), id)
	}
	im.resolved[key] = st
	im.doc.Styles[key] = st
	return key, st
}

// displayStyleName turns Word's internal names ("heading 1", "normal") into
// the names Google Docs shows in its style menu.
func displayStyleName(name, id string) string {
	if name == "" {
		name = id
	}
	words := strings.Fields(name)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	if s := strings.Join(words, " "); s == "Normal" {
		return "Normal text"
	} else {
		return s
	}
}

// charStyleRun applies a character style (w:rStyle) chain to run props.
func (im *importer) charStyleRun(rp *model.RunProps, id string) {
	var chain []*elem
	for cur, n := im.styleEls[id], 0; cur != nil && n < 20; n++ {
		chain = append(chain, cur)
		cur = im.styleEls[cur.val("w:basedOn")]
	}
	for i := len(chain) - 1; i >= 0; i-- {
		applyRPr(rp, chain[i].child("w:rPr"))
	}
}

// applyPPr overlays the properties present in a <w:pPr> onto pp. Absent
// properties leave pp unchanged, which is exactly OOXML's inheritance rule.
func applyPPr(pp *model.ParaProps, pPr *elem) {
	if pPr == nil {
		return
	}
	switch pPr.val("w:jc") {
	case "left", "start":
		pp.Align = "left"
	case "center":
		pp.Align = "center"
	case "right", "end":
		pp.Align = "right"
	case "both", "distribute":
		pp.Align = "justify"
	}
	if ind := pPr.child("w:ind"); ind != nil {
		setTwips(&pp.IndentLeft, firstOf(ind.attrv("w:left"), ind.attrv("w:start")))
		setTwips(&pp.IndentRight, firstOf(ind.attrv("w:right"), ind.attrv("w:end")))
		setTwips(&pp.IndentFirst, ind.attrv("w:firstLine"))
		if h, ok := num(ind.attrv("w:hanging")); ok {
			pp.IndentFirst = -h / twipsPerPt
		}
	}
	if sp := pPr.child("w:spacing"); sp != nil {
		setTwips(&pp.SpaceBefore, sp.attrv("w:before"))
		setTwips(&pp.SpaceAfter, sp.attrv("w:after"))
		if line, ok := num(sp.attrv("w:line")); ok {
			switch sp.attrv("w:lineRule") {
			case "exact", "atLeast":
				pp.LineExact, pp.LineHeight = line/twipsPerPt, 0
			default: // "auto": w:line is in 240ths of a line
				pp.LineHeight, pp.LineExact = round2(line/240), 0
			}
		}
	}
}

// applyRPr overlays the properties present in a <w:rPr> onto rp.
func applyRPr(rp *model.RunProps, rPr *elem) {
	if rPr == nil {
		return
	}
	if f := rPr.child("w:rFonts"); f != nil {
		if name := firstOf(f.attrv("w:ascii"), f.attrv("w:hAnsi")); name != "" {
			rp.FontFamily = name
		}
	}
	if sz, ok := num(rPr.val("w:sz")); ok {
		rp.FontSize = sz / 2
	}
	if on, ok := rPr.toggle("w:b"); ok {
		rp.Bold = on
	}
	if on, ok := rPr.toggle("w:i"); ok {
		rp.Italic = on
	}
	if on, ok := rPr.toggle("w:strike"); ok {
		rp.Strike = on
	}
	if on, ok := rPr.toggle("w:dstrike"); ok && on {
		rp.Strike = true
	}
	if u := rPr.child("w:u"); u != nil {
		// w:u carries the underline style (single, double, wave...);
		// anything but "none" is shown as a plain underline for now.
		rp.Underline = u.attrv("w:val") != "none"
	}
	if c := rPr.val("w:color"); c != "" {
		rp.Color = hexColor(c)
	}
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// hexColor normalizes an OOXML color ("1155cc", "auto") to "#1155cc" or "".
func hexColor(c string) string {
	if len(c) != 6 || strings.EqualFold(c, "auto") {
		return ""
	}
	if _, err := hex.DecodeString(c); err != nil {
		return ""
	}
	return "#" + strings.ToLower(c)
}

// highlightColors maps the named highlight colors of <w:highlight>.
var highlightColors = map[string]string{
	"yellow": "#ffff00", "green": "#00ff00", "cyan": "#00ffff", "magenta": "#ff00ff",
	"blue": "#0000ff", "red": "#ff0000", "darkBlue": "#000080", "darkCyan": "#008080",
	"darkGreen": "#008000", "darkMagenta": "#800080", "darkRed": "#800000",
	"darkYellow": "#808000", "darkGray": "#808080", "lightGray": "#c0c0c0", "black": "#000000",
	"white": "#ffffff",
}

// ---------------------------------------------------------------------------
// Lists

// list registers the list definition for numId and returns its level.
func (im *importer) list(numID string, level int) *model.ListLevel {
	if l, ok := im.doc.Lists[numID]; ok {
		if level < len(l.Levels) {
			return &l.Levels[level]
		}
		return nil
	}
	n := im.nums[numID]
	abs := im.abstract[n.val("w:abstractNumId")]
	if abs == nil {
		return nil
	}
	// A <w:num> may override the start value of individual levels.
	starts := map[string]int{}
	for _, o := range n.children("w:lvlOverride") {
		if s, ok := num(o.val("w:startOverride")); ok {
			starts[o.attrv("w:ilvl")] = int(s)
		}
	}
	l := &model.List{}
	for _, lv := range abs.children("w:lvl") {
		ll := model.ListLevel{
			Format: firstOf(lv.val("w:numFmt"), "decimal"),
			Text:   lv.val("w:lvlText"),
			Start:  1,
		}
		if s, ok := num(lv.val("w:start")); ok {
			ll.Start = int(s)
		}
		if s, ok := starts[lv.attrv("w:ilvl")]; ok {
			ll.Start = s
		}
		var pp model.ParaProps
		applyPPr(&pp, lv.child("w:pPr"))
		ll.IndentLeft, ll.IndentHanging = pp.IndentLeft, -pp.IndentFirst
		if f := lv.child("w:rPr").child("w:rFonts"); f != nil {
			ll.FontFamily = firstOf(f.attrv("w:ascii"), f.attrv("w:hAnsi"))
		}
		l.Levels = append(l.Levels, ll)
	}
	im.doc.Lists[numID] = l
	if level < len(l.Levels) {
		return &l.Levels[level]
	}
	return nil
}

// ---------------------------------------------------------------------------
// Block content

// blocks converts the block-level children of a body or table cell.
func (im *importer) blocks(container *elem) []*model.Node {
	var out []*model.Node
	for _, k := range container.kids {
		switch k.name {
		case "w:p":
			out = append(out, im.paragraph(k))
		case "w:tbl":
			out = append(out, im.table(k))
		case "w:sdt":
			// Structured document tags (content controls) are wrappers;
			// their content is ordinary blocks.
			out = append(out, im.blocks(k.child("w:sdtContent"))...)
		}
	}
	return out
}

func (im *importer) paragraph(p *elem) *model.Node {
	pPr := p.child("w:pPr")
	key, st := im.style(pPr.val("w:pStyle"))
	n := &model.Node{Type: "paragraph"}
	n.SetAttr("style", key)

	// Effective paragraph props: style, then list level indent, then the
	// paragraph's own direct formatting. Only differences from the style
	// are stored.
	eff := st.Para
	if np := pPr.child("w:numPr"); np != nil {
		numID := np.val("w:numId")
		lvl, _ := num(np.val("w:ilvl"))
		// numId 0 is OOXML's way of saying "not a list item".
		if numID != "" && numID != "0" {
			if ll := im.list(numID, int(lvl)); ll != nil {
				n.SetAttr("listId", numID)
				n.SetAttr("listLevel", int(lvl))
				eff.IndentLeft, eff.IndentFirst = ll.IndentLeft, -ll.IndentHanging
			}
		}
	}
	applyPPr(&eff, pPr)
	paraDiff(n, st.Para, eff)
	if on, _ := pPr.toggle("w:pageBreakBefore"); on {
		n.SetAttr("pageBreakBefore", true)
	}

	im.fieldDepth, im.inInstr = 0, false
	var anchors []any
	im.inlines(p, st.Run, "", &n.Content, &anchors)
	if len(anchors) > 0 {
		n.SetAttr("anchors", anchors)
	}
	n.Content = mergeText(n.Content)
	return n
}

func paraDiff(n *model.Node, style, eff model.ParaProps) {
	if eff.Align != style.Align {
		n.SetAttr("align", eff.Align)
	}
	set := func(key string, s, e float64) {
		if s != e {
			n.SetAttr(key, round2(e))
		}
	}
	set("indentLeft", style.IndentLeft, eff.IndentLeft)
	set("indentRight", style.IndentRight, eff.IndentRight)
	set("indentFirst", style.IndentFirst, eff.IndentFirst)
	set("spaceBefore", style.SpaceBefore, eff.SpaceBefore)
	set("spaceAfter", style.SpaceAfter, eff.SpaceAfter)
	set("lineHeight", style.LineHeight, eff.LineHeight)
	set("lineExact", style.LineExact, eff.LineExact)
}

// inlines walks the inline content of a paragraph (or hyperlink etc.),
// appending nodes to out. link is the href of an enclosing hyperlink.
func (im *importer) inlines(parent *elem, styleRun model.RunProps, link string, out *[]*model.Node, anchors *[]any) {
	for _, k := range parent.kids {
		switch k.name {
		case "w:r":
			im.run(k, styleRun, link, out)
		case "w:hyperlink":
			href := ""
			if r, ok := im.rels[k.attrv("r:id")]; ok && r.external {
				href = r.target
			}
			if a := k.attrv("w:anchor"); a != "" {
				href = "#" + a
			}
			im.inlines(k, styleRun, href, out, anchors)
		case "w:bookmarkStart":
			// Google gives every heading a bookmark so its table of
			// contents and internal links can point at it. _GoBack is
			// Word's "last edit position" marker, not a real target.
			if name := k.attrv("w:name"); name != "" && name != "_GoBack" {
				*anchors = append(*anchors, name)
			}
		case "w:ins", "w:smartTag", "w:fldSimple", "w:customXml":
			// Wrappers whose content is ordinary runs. (w:del is
			// deliberately not here: deleted text should not show.)
			im.inlines(k, styleRun, link, out, anchors)
		case "w:sdt":
			im.inlines(k.child("w:sdtContent"), styleRun, link, out, anchors)
		}
	}
}

func (im *importer) run(r *elem, styleRun model.RunProps, link string, out *[]*model.Node) {
	rPr := r.child("w:rPr")
	eff := styleRun
	if cs := rPr.val("w:rStyle"); cs != "" {
		im.charStyleRun(&eff, cs)
	}
	applyRPr(&eff, rPr)
	marks := runMarks(styleRun, eff, rPr, link)

	text := func(s string) {
		if s == "" || im.inInstr {
			return
		}
		*out = append(*out, &model.Node{Type: "text", Text: s, Marks: marks})
	}
	for _, k := range r.kids {
		switch k.name {
		case "w:t":
			text(k.text)
		case "w:tab":
			text("\t")
		case "w:noBreakHyphen":
			text("‑")
		case "w:br", "w:cr":
			if im.inInstr {
				continue
			}
			if k.attrv("w:type") == "page" {
				*out = append(*out, &model.Node{Type: "pageBreak"})
			} else {
				*out = append(*out, &model.Node{Type: "hardBreak"})
			}
		case "w:drawing":
			if img := im.image(k); img != nil && !im.inInstr {
				if link != "" {
					img.SetAttr("href", link)
				}
				*out = append(*out, img)
			}
		case "w:fldChar":
			switch k.attrv("w:fldCharType") {
			case "begin":
				im.fieldDepth++
				im.inInstr = true
			case "separate":
				im.inInstr = false
			case "end":
				if im.fieldDepth > 0 {
					im.fieldDepth--
				}
				im.inInstr = false
			}
		}
	}
}

// runMarks works out the marks a run needs: whatever differs between its
// paragraph style's character formatting and its own effective formatting,
// plus the properties that only ever come from direct formatting.
func runMarks(style, eff model.RunProps, rPr *elem, link string) []*model.Mark {
	var marks []*model.Mark
	ts := map[string]any{}
	flag := func(name string, s, e bool, offKey, offVal string) {
		switch {
		case e && !s:
			marks = append(marks, &model.Mark{Type: name})
		case !e && s:
			ts[offKey] = offVal
		}
	}
	flag("bold", style.Bold, eff.Bold, "fontWeight", "normal")
	flag("italic", style.Italic, eff.Italic, "fontStyle", "normal")
	flag("underline", style.Underline, eff.Underline, "textDecoration", "none")
	flag("strike", style.Strike, eff.Strike, "textDecoration", "none")
	switch rPr.val("w:vertAlign") {
	case "superscript":
		marks = append(marks, &model.Mark{Type: "superscript"})
	case "subscript":
		marks = append(marks, &model.Mark{Type: "subscript"})
	}
	if eff.FontFamily != style.FontFamily {
		ts["fontFamily"] = eff.FontFamily
	}
	if eff.FontSize != style.FontSize {
		ts["fontSize"] = eff.FontSize
	}
	if eff.Color != style.Color {
		ts["color"] = firstOf(eff.Color, "#000000")
	}
	if len(ts) > 0 {
		marks = append(marks, &model.Mark{Type: "textStyle", Attrs: ts})
	}
	hl := highlightColors[rPr.val("w:highlight")]
	if fill := hexColor(rPr.child("w:shd").attrv("w:fill")); fill != "" {
		hl = fill
	}
	if hl != "" {
		marks = append(marks, &model.Mark{Type: "highlight", Attrs: map[string]any{"color": hl}})
	}
	if link != "" {
		marks = append(marks, &model.Mark{Type: "link", Attrs: map[string]any{"href": link}})
	}
	return marks
}

// mergeText joins adjacent text nodes that carry identical marks. Google's
// export splits text into many runs with the same formatting (one per edit
// session, roughly); merging them keeps doc.json and the HTML small and
// gives the future editor clean text to work with.
func mergeText(nodes []*model.Node) []*model.Node {
	var out []*model.Node
	for _, n := range nodes {
		if len(out) > 0 {
			last := out[len(out)-1]
			if n.Type == "text" && last.Type == "text" && reflect.DeepEqual(n.Marks, last.Marks) {
				last.Text += n.Text
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// ---------------------------------------------------------------------------
// Images

func (im *importer) image(drawing *elem) *model.Node {
	inline := drawing.child("wp:inline")
	anchor := drawing.child("wp:anchor")
	box := inline
	if box == nil {
		box = anchor
	}
	blip := box.find("a:blip")
	if blip == nil {
		return nil // a chart or shape rather than a picture
	}
	src := im.storeImage(blip.attrv("r:embed"))
	if src == "" {
		return nil
	}

	ext := box.child("wp:extent")
	w, h := emu(ext.attrv("cx")), emu(ext.attrv("cy"))
	n := &model.Node{Type: "image"}
	n.SetAttr("src", "img/"+src)
	n.SetAttr("width", w)
	n.SetAttr("height", h)
	if pr := box.child("wp:docPr"); pr != nil {
		if d := pr.attrv("descr"); d != "" {
			n.SetAttr("alt", d)
		}
		if t := pr.attrv("title"); t != "" {
			n.SetAttr("title", t)
		}
	}

	// Cropping: <a:srcRect> gives how much of each edge of the original
	// image is cut off, in 1/1000ths of a percent. Stored as fractions.
	if sr := box.find("a:srcRect"); sr != nil {
		crop := map[string]any{}
		for attr, key := range map[string]string{"l": "left", "t": "top", "r": "right", "b": "bottom"} {
			if v, ok := num(sr.attrv(attr)); ok && v != 0 {
				crop[key] = v / 100000
			}
		}
		if len(crop) > 0 {
			n.SetAttr("crop", crop)
		}
	}

	if anchor == nil {
		n.SetAttr("wrap", "inline")
		return n
	}

	// Anchored ("wrap text", "break text", "behind/in front of text")
	// images are positioned relative to the page, margin, column or
	// paragraph. HTML can't position a box and flow text around it at an
	// arbitrary spot, so we reduce it to what CSS floats can do: which side
	// of the column the image sits on, and its offset from that side.
	wrap := "none"
	switch {
	case anchor.child("wp:wrapSquare") != nil:
		wrap = "square"
	case anchor.child("wp:wrapTight") != nil:
		wrap = "tight"
	case anchor.child("wp:wrapThrough") != nil:
		wrap = "through"
	case anchor.child("wp:wrapTopAndBottom") != nil:
		wrap = "topBottom"
	}
	n.SetAttr("wrap", wrap)
	if wrap == "none" && anchor.attrv("behindDoc") == "1" {
		n.SetAttr("behind", true)
	}

	pg := im.doc.Page
	colW := pg.Width - pg.MarginLeft - pg.MarginRight
	ph := anchor.child("wp:positionH")
	x := 0.0
	if off := ph.child("wp:posOffset"); off != nil {
		x = emu(off.text)
		if ph.attrv("relativeFrom") == "page" {
			x -= pg.MarginLeft
		}
	} else {
		switch ph.child("wp:align").text {
		case "right", "outside":
			x = colW - w
		case "center":
			x = (colW - w) / 2
		}
	}
	side := "left"
	if x+w/2 > colW/2 {
		side = "right"
	}
	n.SetAttr("side", side)
	n.SetAttr("offsetX", round2(x))
	// Vertical position is only meaningful to us relative to the
	// paragraph (or line) the image is anchored in; offsets from the top
	// of the page or margin depend on pagination, which the continuous
	// HTML view doesn't have.
	pv := anchor.child("wp:positionV")
	if off := pv.child("wp:posOffset"); off != nil {
		switch pv.attrv("relativeFrom") {
		case "paragraph", "line":
			n.SetAttr("offsetY", emu(off.text))
		}
	}
	for attr, key := range map[string]string{"distT": "distTop", "distR": "distRight", "distB": "distBottom", "distL": "distLeft"} {
		if v := emu(anchor.attrv(attr)); v != 0 {
			n.SetAttr(key, v)
		}
	}
	return n
}

// storeImage copies the image behind a relationship id into the result and
// returns its file name. Names are derived from the content hash, which
// de-duplicates repeated images and means an image file, once written, is
// never overwritten with different bytes (images are WORM like history).
func (im *importer) storeImage(relID string) string {
	if name, ok := im.imgName[relID]; ok {
		return name
	}
	r, ok := im.rels[relID]
	if !ok || r.external {
		return ""
	}
	ext := strings.ToLower(path.Ext(r.target))
	if !imageTypes[ext] {
		return ""
	}
	// Targets are relative to word/, e.g. "media/image1.png".
	data, err := im.read(path.Join("word", r.target))
	if err != nil {
		return ""
	}
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:20] + ext
	im.images[name] = data
	im.imgName[relID] = name
	return name
}

// ---------------------------------------------------------------------------
// Tables

// borderCSS converts a border element (w:top etc.) to a CSS border value.
func borderCSS(b *elem) (string, bool) {
	if b == nil {
		return "", false
	}
	switch b.attrv("w:val") {
	case "nil", "none", "":
		return "none", true
	}
	w := 0.75
	if sz, ok := num(b.attrv("w:sz")); ok {
		w = sz / 8 // eighths of a point
	}
	c := firstOf(hexColor(b.attrv("w:color")), "#000000")
	return fmt.Sprintf("%gpt solid %s", round2(w), c), true
}

func (im *importer) table(tbl *elem) *model.Node {
	t := &model.Node{Type: "table"}
	tblPr := tbl.child("w:tblPr")

	var cols []any
	for _, gc := range tbl.child("w:tblGrid").children("w:gridCol") {
		w, _ := num(gc.attrv("w:w"))
		cols = append(cols, round2(w/twipsPerPt))
	}
	if len(cols) > 0 {
		t.SetAttr("colWidths", cols)
	}
	if mar := tblPr.child("w:tblCellMar"); mar != nil {
		pad := []any{}
		for _, side := range []string{"w:top", "w:right", "w:bottom", "w:left"} {
			v, _ := num(firstOf(mar.child(side).attrv("w:w"), "0"))
			pad = append(pad, round2(v/twipsPerPt))
		}
		t.SetAttr("cellPadding", pad)
	}

	// First pass: collect cells with their grid position, so vertical
	// merges (which OOXML expresses as "continue the cell above") can be
	// turned into HTML rowspans.
	type cell struct {
		node     *model.Node
		col      int
		span     int
		vMerge   string // "", "restart" or "continue"
		rowspan  int
		consumed bool
	}
	var rows [][]*cell
	var rowNodes []*model.Node
	tblBorders := tblPr.child("w:tblBorders")
	trs := tbl.children("w:tr")
	for ri, tr := range trs {
		row := &model.Node{Type: "tableRow"}
		if h, ok := num(tr.child("w:trPr").child("w:trHeight").attrv("w:val")); ok {
			row.SetAttr("height", round2(h/twipsPerPt))
		}
		var cells []*cell
		col := 0
		for _, tc := range tr.children("w:tc") {
			tcPr := tc.child("w:tcPr")
			c := &cell{node: &model.Node{Type: "tableCell"}, col: col, span: 1, rowspan: 1}
			if s, ok := num(tcPr.val("w:gridSpan")); ok && s > 1 {
				c.span = int(s)
				c.node.SetAttr("colspan", c.span)
			}
			if vm := tcPr.child("w:vMerge"); vm != nil {
				c.vMerge = "continue"
				if vm.attrv("w:val") == "restart" {
					c.vMerge = "restart"
				}
			}
			if w, ok := num(tcPr.child("w:tcW").attrv("w:w")); ok && tcPr.child("w:tcW").attrv("w:type") == "dxa" {
				c.node.SetAttr("width", round2(w/twipsPerPt))
			}
			if fill := hexColor(tcPr.child("w:shd").attrv("w:fill")); fill != "" {
				c.node.SetAttr("background", fill)
			}
			if va := tcPr.val("w:vAlign"); va != "" {
				c.node.SetAttr("verticalAlign", va)
			}
			// Cell borders: the cell's own setting wins; otherwise the
			// table's outer border on the table's edges and its inside
			// border between cells.
			tcb := tcPr.child("w:tcBorders")
			lastRow, lastCol := ri == len(trs)-1, col+c.span >= len(cols)
			for _, s := range []struct {
				key, own, edge, inside string
				isEdge                 bool
			}{
				{"borderTop", "w:top", "w:top", "w:insideH", ri == 0},
				{"borderBottom", "w:bottom", "w:bottom", "w:insideH", lastRow},
				{"borderLeft", "w:left", "w:left", "w:insideV", col == 0},
				{"borderRight", "w:right", "w:right", "w:insideV", lastCol},
			} {
				css, ok := borderCSS(tcb.child(s.own))
				if !ok {
					tb := s.inside
					if s.isEdge {
						tb = s.edge
					}
					css, ok = borderCSS(tblBorders.child(tb))
				}
				if ok {
					c.node.SetAttr(s.key, css)
				}
			}
			c.node.Content = im.blocks(tc)
			cells = append(cells, c)
			col += c.span
		}
		rows = append(rows, cells)
		rowNodes = append(rowNodes, row)
	}

	// Second pass: each "restart" cell absorbs the "continue" cells below
	// it in the same grid column.
	for ri, cells := range rows {
		for _, c := range cells {
			if c.vMerge != "restart" {
				continue
			}
			for rj := ri + 1; rj < len(rows); rj++ {
				var below *cell
				for _, b := range rows[rj] {
					if b.col == c.col {
						below = b
					}
				}
				if below == nil || below.vMerge != "continue" {
					break
				}
				below.consumed = true
				c.rowspan++
			}
			if c.rowspan > 1 {
				c.node.SetAttr("rowspan", c.rowspan)
			}
		}
	}
	for ri, cells := range rows {
		for _, c := range cells {
			if !c.consumed {
				rowNodes[ri].Content = append(rowNodes[ri].Content, c.node)
			}
		}
		t.Content = append(t.Content, rowNodes[ri])
	}
	return t
}

// ---------------------------------------------------------------------------
// Title fallback

func firstHeading(doc *model.Node) string {
	var fallback string
	for _, b := range doc.Content {
		if b.Type != "paragraph" {
			continue
		}
		var sb strings.Builder
		for _, in := range b.Content {
			sb.WriteString(in.Text)
		}
		t := strings.TrimSpace(sb.String())
		if t == "" {
			continue
		}
		if s := b.Str("style"); s == "title" || strings.HasPrefix(s, "heading") {
			return t
		}
		if fallback == "" {
			fallback = t
		}
	}
	return fallback
}
