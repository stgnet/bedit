// Package model defines bedit's document format: what doc.json holds on disk
// and what the renderer and (later) the editor work from.
//
// Why this shape:
//
//   - The body is a ProseMirror-style JSON tree (type / attrs / content /
//     text / marks). The live collaborative editor planned for a later step
//     will almost certainly be ProseMirror (or TipTap, built on it). Storing
//     the document in the shape it already speaks means no conversion layer,
//     and ProseMirror's collab model describes every edit as a small "step"
//     that maps directly onto bedit's append-only history log.
//
//   - Document-wide things (page size, named styles, list definitions) sit
//     beside the tree rather than inside it. That mirrors Google Docs, where
//     "Heading 1" is a named style you can redefine for the whole document,
//     and list numbering is defined once and referenced by each list item.
//
//   - Lists are kept flat: a list item is just a paragraph with listId and
//     listLevel attrs, the way Google Docs (and .docx) model them. Nesting
//     <ul>/<ol> trees would have to be invented on import and flattened again
//     on export, and Google's mixed-level numbering ("1.", "a.", "i.") is
//     easier to reproduce from a flat run of paragraphs.
//
// All lengths are in points (1/72 inch), the unit both Google Docs and CSS
// can express exactly.
package model

// FormatVersion is bumped whenever doc.json changes incompatibly, so old
// documents can be recognized and migrated (or re-imported from their
// preserved original) instead of being misread.
const FormatVersion = 1

// Document is the full content of doc.json.
type Document struct {
	Format int               `json:"format"`
	Title  string            `json:"title"`
	Page   Page              `json:"page"`
	Styles map[string]*Style `json:"styles"`
	Lists  map[string]*List  `json:"lists,omitempty"`
	Body   *Node             `json:"body"` // root node, Type "doc"
}

// Page is the page geometry. bedit's HTML view is one continuous page (like
// Google's "pageless" mode) but uses the width and side margins so line
// lengths, and therefore line breaks and image wrap, match the original.
// Height and the top/bottom margins matter later for PDF output.
type Page struct {
	Width        float64 `json:"width"`
	Height       float64 `json:"height"`
	MarginTop    float64 `json:"marginTop"`
	MarginBottom float64 `json:"marginBottom"`
	MarginLeft   float64 `json:"marginLeft"`
	MarginRight  float64 `json:"marginRight"`
}

// Style is a named paragraph style ("normal", "title", "heading1", ...).
//
// Styles are stored fully resolved: every field has its final value, with
// the source format's inheritance (docDefaults -> Normal -> Heading 1 ...)
// already flattened. That keeps the renderer trivial (one CSS rule per
// style) and means paragraphs only need to carry what differs from their
// style.
type Style struct {
	Name string    `json:"name"` // display name, e.g. "Heading 1"
	Para ParaProps `json:"para"`
	Run  RunProps  `json:"run"`
}

// ParaProps are paragraph-level formatting values.
type ParaProps struct {
	Align       string  `json:"align"`       // left, center, right, justify
	IndentLeft  float64 `json:"indentLeft"`  // pt
	IndentRight float64 `json:"indentRight"` // pt
	IndentFirst float64 `json:"indentFirst"` // pt; negative means a hanging indent
	SpaceBefore float64 `json:"spaceBefore"` // pt
	SpaceAfter  float64 `json:"spaceAfter"`  // pt
	LineHeight  float64 `json:"lineHeight"`  // multiple of single spacing (1.15 etc); 0 if LineExact is used
	LineExact   float64 `json:"lineExact"`   // pt, for exact / at-least line spacing; 0 if unused
}

// RunProps are character-level formatting values.
type RunProps struct {
	FontFamily string  `json:"fontFamily"`
	FontSize   float64 `json:"fontSize"` // pt
	Bold       bool    `json:"bold"`
	Italic     bool    `json:"italic"`
	Underline  bool    `json:"underline"`
	Strike     bool    `json:"strike"`
	Color      string  `json:"color"` // "#rrggbb", or "" for the default (black)
}

// List defines the marker for each nesting level of one list.
type List struct {
	Levels []ListLevel `json:"levels"`
}

// ListLevel is one level of a list definition.
type ListLevel struct {
	// Format is how the counter is written: bullet, decimal, lowerLetter,
	// upperLetter, lowerRoman, upperRoman, or none.
	Format string `json:"format"`
	// Text is the marker template. For bullets it is the glyph itself
	// ("●"); for numbered levels it holds %1..%9 placeholders for the
	// counters of each level, e.g. "%1." or "%1.%2.".
	Text  string `json:"text"`
	Start int    `json:"start"`
	// Default indent for items at this level; paragraphs may override.
	IndentLeft    float64 `json:"indentLeft"`
	IndentHanging float64 `json:"indentHanging"`
	// Marker font, when the list specifies one (some bullet glyphs depend
	// on it). Empty means the marker uses the paragraph's own font.
	FontFamily string `json:"fontFamily,omitempty"`
}

// Node is one node of the ProseMirror-style body tree.
//
// Node types used so far (attrs in parentheses):
//
//	doc        root; content is block nodes
//	paragraph  (style, align, indentLeft, indentRight, indentFirst,
//	           spaceBefore, spaceAfter, lineHeight, lineExact,
//	           listId, listLevel, anchors, pageBreakBefore)
//	           content is inline nodes
//	table      (colWidths) content is tableRow
//	tableRow   (height) content is tableCell
//	tableCell  (colspan, rowspan, width, background, verticalAlign,
//	           borderTop/Right/Bottom/Left, padding) content is block nodes
//	text       Text plus Marks
//	image      (src, width, height, alt, title, crop, wrap, side,
//	           offsetX, offsetY, distTop, distRight, distBottom, distLeft)
//	hardBreak  a line break within a paragraph (Shift+Enter)
//	pageBreak  an explicit page break
//
// Paragraph attrs are only present when they differ from the paragraph's
// named style. Attr values are plain JSON values (string, float64, bool,
// []any, map[string]any) so the tree round-trips through JSON unchanged.
type Node struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []*Node        `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []*Mark        `json:"marks,omitempty"`
}

// Mark is formatting applied to a text node.
//
//	bold, italic, underline, strike, superscript, subscript   (no attrs)
//	link       (href)
//	textStyle  (fontFamily, fontSize, color, and the overrides fontWeight,
//	           fontStyle, textDecoration set to "normal"/"none" when text
//	           turns off something its paragraph style turns on, e.g. a
//	           non-bold word inside a bold heading)
//	highlight  (color)
type Mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// SetAttr sets an attribute, creating the map on first use.
func (n *Node) SetAttr(key string, v any) {
	if n.Attrs == nil {
		n.Attrs = map[string]any{}
	}
	n.Attrs[key] = v
}

// Str returns a string attribute, or "" if absent.
func (n *Node) Str(key string) string {
	s, _ := n.Attrs[key].(string)
	return s
}

// Num returns a numeric attribute and whether it was present. Numbers come
// back from JSON as float64 but may be int when the node was built in Go,
// so both are accepted.
func (n *Node) Num(key string) (float64, bool) {
	return Number(n.Attrs[key])
}

// Number converts a JSON-ish numeric value to float64.
func Number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// Bool returns a boolean attribute, false if absent.
func (n *Node) Bool(key string) bool {
	b, _ := n.Attrs[key].(bool)
	return b
}
