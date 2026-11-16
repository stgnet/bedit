package render

import (
	"strings"
	"testing"

	"github.com/stgnet/bedit/internal/docx"
	"github.com/stgnet/bedit/internal/docxtest"
	"github.com/stgnet/bedit/internal/model"
)

func renderSample(t *testing.T) *Page {
	res, err := docx.Import(docxtest.Sample(), "")
	if err != nil {
		t.Fatal(err)
	}
	return Render(res.Doc, "/bedit/sample/")
}

func TestRenderSample(t *testing.T) {
	p := renderSample(t)
	body := string(p.Body)
	for _, want := range []string{
		`<h1 class="s-heading1" id="_h1">`,
		`<span style="font-weight:400">plain</span>`,
		`<a href="https://example.com/">link</a>`,
		`<a href="#_h1">back</a>`,
		`>1.</span>one</span>`, // marker glued to the first word
		`>a.</span>one-a</span>`,
		`>2.</span>two</span>`, // level 0 continues after the nested item
		`float:right`,
		`clear:right`, // pushed down by its offsetY spacer
		`src="/bedit/sample/img/`,
		`width:125.0000%`, // 10% cropped off each side
		`rowspan="2"`,
		`background-color:#ffff00`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s", want)
		}
	}
	css := string(p.CSS)
	if !strings.Contains(css, ".page .s-heading1{font-family:'Arial',sans-serif;font-size:20pt;font-weight:700") {
		t.Errorf("css = %s", css)
	}
	if len(p.Fonts) != 0 {
		t.Errorf("Arial is a system font, but got font links %v", p.Fonts)
	}
}

// A document is untrusted input: text, link targets, fonts and image
// sources must not be able to inject markup or script.
func TestRenderEscapesHostileContent(t *testing.T) {
	doc := &model.Document{
		Page:   model.Page{Width: 612, MarginLeft: 72, MarginRight: 72},
		Styles: map[string]*model.Style{"normal": {}},
		Body: &model.Node{Type: "doc", Content: []*model.Node{{
			Type:  "paragraph",
			Attrs: map[string]any{"style": "normal", "anchors": []any{`x" onmouseover="alert(1)`}},
			Content: []*model.Node{
				{Type: "text", Text: "<script>alert(1)</script>"},
				{Type: "text", Text: "js", Marks: []*model.Mark{{Type: "link", Attrs: map[string]any{"href": "javascript:alert(1)"}}}},
				{Type: "text", Text: "font", Marks: []*model.Mark{{Type: "textStyle", Attrs: map[string]any{"fontFamily": "x;}</style><script>", "color": "red;background:url(x)"}}}},
				{Type: "image", Attrs: map[string]any{"src": "../../etc/passwd", "width": 10.0, "height": 10.0, "wrap": "inline"}},
			},
		}}},
	}
	body := string(Render(doc, "/b/d/").Body)
	for _, bad := range []string{"<script", "javascript:", "</style>", "passwd", `" onmouseover`, "url(x)"} {
		if strings.Contains(body, bad) {
			t.Errorf("rendered output contains %q:\n%s", bad, body)
		}
	}
}

func TestFontLinks(t *testing.T) {
	r := &renderer{fonts: map[string]bool{"Arial": true, "Open Sans": true}}
	links := r.fontLinks()
	if len(links) != 2 || !strings.Contains(links[0], "family=Open+Sans&") || !strings.Contains(links[1], "Open+Sans:ital,wght@") {
		t.Errorf("links = %v", links)
	}
}

func TestCounters(t *testing.T) {
	cases := map[string]string{
		formatCounter(3, "lowerLetter"):   "c",
		formatCounter(28, "upperLetter"):  "BB",
		formatCounter(4, "lowerRoman"):    "iv",
		formatCounter(1994, "upperRoman"): "MCMXCIV",
		formatCounter(7, "decimal"):       "7",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
