package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stgnet/bedit/internal/model"
)

func testDoc(title string) *model.Document {
	return &model.Document{Format: model.FormatVersion, Title: title, Body: &model.Node{Type: "doc"}}
}

func TestCreateAndHistory(t *testing.T) {
	s := New(t.TempDir())
	imgs := map[string][]byte{"0123456789abcdef0123.png": []byte("png")}
	name, err := s.Create("my-doc", false, &Meta{Title: "My Doc", Owners: []string{"u1"}, Public: PublicView},
		testDoc("v1"), imgs, []byte("docx-1"), "u1", "upload:a.docx")
	if err != nil || name != "my-doc" {
		t.Fatalf("create: %q %v", name, err)
	}

	// A chosen name that is taken is an error; a suggested one gets -2.
	if _, err := s.Create("my-doc", false, &Meta{}, testDoc("x"), nil, nil, "u1", ""); err != ErrExists {
		t.Errorf("duplicate chosen name: err = %v", err)
	}
	if n, err := s.Create("my-doc", true, &Meta{}, testDoc("x"), nil, nil, "u1", ""); err != nil || n != "my-doc-2" {
		t.Errorf("suggested name: %q %v", n, err)
	}

	// Re-import appends to history and replaces doc.json.
	if err := s.Import("my-doc", testDoc("v2"), imgs, []byte("docx-2"), "u1", "upload:a.docx"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Doc("my-doc")
	if err != nil || d.Title != "v2" {
		t.Errorf("doc = %+v %v", d, err)
	}
	hist, _ := os.ReadFile(filepath.Join(s.Root, "my-doc", "history.jsonl"))
	lines := bytes.Split(bytes.TrimSpace(hist), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("history has %d entries", len(lines))
	}
	var e Entry
	json.Unmarshal(lines[1], &e)
	if e.Seq != 2 || e.Op != "import" || e.Doc.Title != "v2" || e.Original != "src/2.docx" {
		t.Errorf("entry 2 = %+v", e)
	}
	if orig, _ := s.LatestOriginal("my-doc"); string(orig) != "docx-2" {
		t.Errorf("latest original = %q", orig)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"My Trip (2024)":   "my-trip-2024",
		"  --Hello__World": "hello-world",
		"日本語":              "",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "_users", "Upper", "a/b", "..", "-x", strings.Repeat("a", 81)} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q) = true", bad)
		}
	}
	s := New(t.TempDir())
	if p := s.ImagePath("doc", "../meta.json"); p != "" {
		t.Errorf("ImagePath allowed traversal: %q", p)
	}
}

func TestAccess(t *testing.T) {
	m := &Meta{Owners: []string{"o"}, Editors: []string{"e"}, Public: PublicPrivate}
	if m.Role("o") != "owner" || m.Role("e") != "editor" || m.Role("x") != "" || m.Role("") != "" {
		t.Error("roles wrong")
	}
	if !m.CanView("e") || m.CanView("x") || m.Listed("x") {
		t.Error("private doc access wrong")
	}
	m.Public = PublicNone
	if m.CanView("x") || !m.Listed("x") {
		t.Error("invite-only doc access wrong")
	}
	m.Public = PublicView
	if !m.CanView("") {
		t.Error("public view doc should be visible anonymously")
	}
}
