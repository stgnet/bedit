package docx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stgnet/bedit/internal/docxtest"
	"github.com/stgnet/bedit/internal/model"
)

func TestImportSample(t *testing.T) {
	res, err := Import(docxtest.Sample(), "")
	if err != nil {
		t.Fatal(err)
	}
	d := res.Doc
	if d.Title != "Big plain" {
		t.Errorf("title from first heading = %q", d.Title)
	}
	if d.Page.Width != 612 || d.Page.MarginLeft != 72 {
		t.Errorf("page = %+v", d.Page)
	}

	// Styles are resolved through basedOn and docDefaults.
	h1 := d.Styles["heading1"]
	if h1 == nil || !h1.Run.Bold || h1.Run.FontSize != 20 || h1.Run.FontFamily != "Arial" || h1.Para.SpaceBefore != 20 || h1.Para.LineHeight != 1.15 {
		t.Errorf("heading1 = %+v", h1)
	}

	body := d.Body.Content
	// Heading: bookmark kept, and "plain" un-bolds against the bold style.
	if a, _ := body[0].Attrs["anchors"].([]any); len(a) != 1 || a[0] != "_h1" {
		t.Errorf("anchors = %v", body[0].Attrs["anchors"])
	}
	if got := marksJSON(body[0].Content[1]); got != `[{"type":"textStyle","attrs":{"fontWeight":"normal"}}]` {
		t.Errorf("un-bold marks = %s", got)
	}

	// Split runs with equal formatting are merged.
	p := body[1].Content
	if p[0].Text != "Hello world, " || p[0].Marks != nil {
		t.Errorf("merged text = %q %v", p[0].Text, p[0].Marks)
	}
	if got := marksJSON(p[1]); got != `[{"type":"italic"},{"type":"textStyle","attrs":{"color":"#ff0000"}}]` {
		t.Errorf("red italic marks = %s", got)
	}
	if got := marksJSON(p[2]); !strings.Contains(got, `"href":"https://example.com/"`) {
		t.Errorf("external link marks = %s", got)
	}
	if got := marksJSON(p[3]); !strings.Contains(got, `"href":"#_h1"`) {
		t.Errorf("internal link marks = %s", got)
	}

	// List items.
	if body[3].Str("listId") != "1" || body[3].Attrs["listLevel"] != 1 {
		t.Errorf("list attrs = %v", body[3].Attrs)
	}
	if l := d.Lists["1"]; l == nil || l.Levels[1].Format != "lowerLetter" || l.Levels[0].IndentLeft != 36 || l.Levels[0].IndentHanging != 18 {
		t.Errorf("list def = %+v", d.Lists["1"])
	}

	// The wrapped, cropped image.
	img := body[5].Content[1]
	if img.Type != "image" || img.Str("wrap") != "square" || img.Str("side") != "right" || img.Str("alt") != "a photo" {
		t.Fatalf("image = %+v", img.Attrs)
	}
	if y, _ := img.Num("offsetY"); y != 13.5 {
		t.Errorf("offsetY = %v", y)
	}
	src := strings.TrimPrefix(img.Str("src"), "img/")
	if _, ok := res.Images[src]; !ok || !strings.HasSuffix(src, ".png") {
		t.Errorf("image %q not in result %v", src, res.Images)
	}
	if crop, _ := img.Attrs["crop"].(map[string]any); crop["left"] != 0.1 || crop["right"] != 0.1 {
		t.Errorf("crop = %v", img.Attrs["crop"])
	}

	// Table: vertical merge becomes rowspan; the merged-away cell is gone.
	tbl := body[6]
	if tbl.Type != "table" || len(tbl.Content) != 2 {
		t.Fatalf("table = %+v", tbl)
	}
	a := tbl.Content[0].Content[0]
	if a.Attrs["rowspan"] != 2 || a.Str("background") != "#ffff00" || a.Str("borderTop") != "1pt solid #000000" {
		t.Errorf("cell A = %v", a.Attrs)
	}
	if n := len(tbl.Content[1].Content); n != 1 {
		t.Errorf("second row has %d cells, want 1", n)
	}
}

func TestImportTitleArgumentWins(t *testing.T) {
	res, err := Import(docxtest.Sample(), "  From Filename ")
	if err != nil {
		t.Fatal(err)
	}
	if res.Doc.Title != "From Filename" {
		t.Errorf("title = %q", res.Doc.Title)
	}
}

func TestImportFieldsShowResultOnly(t *testing.T) {
	body := `<w:p><w:r><w:t xml:space="preserve">Page </w:t></w:r>
<w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText>PAGE</w:instrText></w:r>
<w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>7</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>`
	res, err := Import(docxtest.Build(body), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Doc.Body.Content[0].Content[0].Text; got != "Page 7" {
		t.Errorf("field text = %q", got)
	}
}

func TestImportRejectsNonDocx(t *testing.T) {
	if _, err := Import([]byte("<html>sign in</html>"), ""); err == nil {
		t.Error("expected an error for a non-zip file")
	}
}

func marksJSON(n *model.Node) string {
	b, _ := json.Marshal(n.Marks)
	return string(b)
}
