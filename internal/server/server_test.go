package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stgnet/bedit/internal/docxtest"
	"github.com/stgnet/bedit/internal/store"
)

// client is a browser stand-in with its own cookie jar.
func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func upload(t *testing.T, c *http.Client, u, name, public string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "Trip Notes.docx")
	fw.Write(docxtest.Sample())
	mw.WriteField("name", name)
	mw.WriteField("public", public)
	mw.Close()
	resp, err := c.Post(u, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestImportAndView(t *testing.T) {
	ts := httptest.NewServer(New(store.New(t.TempDir()), "/bedit").Handler())
	defer ts.Close()
	owner, stranger := client(t), client(t)

	// Importing needs a log-in.
	if resp := upload(t, owner, ts.URL+"/bedit/_import", "", "private"); !strings.Contains(resp.Request.URL.Path, "/_login") {
		t.Fatalf("anonymous import ended at %s", resp.Request.URL)
	}
	resp, err := owner.PostForm(ts.URL+"/bedit/_login", url.Values{"name": {"Scott"}, "next": {"/bedit/"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The name is suggested from the title (the upload's file name).
	resp = upload(t, owner, ts.URL+"/bedit/_import", "", "private")
	if resp.Request.URL.Path != "/bedit/trip-notes" {
		t.Fatalf("import landed on %s (status %d)", resp.Request.URL.Path, resp.StatusCode)
	}
	code, body := get(t, owner, ts.URL+"/bedit/trip-notes")
	if code != 200 || !strings.Contains(body, `class="s-heading1"`) || !strings.Contains(body, "Re-import") {
		t.Errorf("owner view: %d\n%s", code, body)
	}

	// The image is served to the owner.
	i := strings.Index(body, `/bedit/trip-notes/img/`)
	src := body[i : i+strings.Index(body[i:], `"`)]
	if code, _ := get(t, owner, ts.URL+src); code != 200 {
		t.Errorf("image %s: %d", src, code)
	}

	// Private: strangers get neither the doc, its image, nor a list entry.
	if code, _ := get(t, stranger, ts.URL+"/bedit/trip-notes"); code != 403 {
		t.Errorf("stranger view: %d", code)
	}
	if code, _ := get(t, stranger, ts.URL+src); code != 403 {
		t.Errorf("stranger image: %d", code)
	}
	if _, idx := get(t, stranger, ts.URL+"/bedit/"); strings.Contains(idx, "trip-notes") {
		t.Error("private doc listed for a stranger")
	}

	// A public doc under a chosen name is visible to anyone.
	resp = upload(t, owner, ts.URL+"/bedit/_import", "shared", "view")
	if resp.Request.URL.Path != "/bedit/shared" {
		t.Fatalf("named import landed on %s", resp.Request.URL.Path)
	}
	if code, _ := get(t, stranger, ts.URL+"/bedit/shared"); code != 200 {
		t.Errorf("stranger view of public doc: %d", code)
	}
	// Choosing a taken name is refused rather than silently renamed.
	resp = upload(t, owner, ts.URL+"/bedit/_import", "shared", "view")
	if resp.StatusCode != 400 {
		t.Errorf("duplicate name: status %d", resp.StatusCode)
	}

	if code, _ := get(t, owner, ts.URL+"/bedit/nope"); code != 404 {
		t.Errorf("missing doc: %d", code)
	}
}

func TestSafeNext(t *testing.T) {
	s := &Server{prefix: "/bedit"}
	for in, want := range map[string]string{
		"/bedit/doc":        "/bedit/doc",
		"https://evil.com/": "/bedit/",
		"//evil.com/bedit/": "/bedit/",
		"/bedit/\\evil.com": "/bedit/",
	} {
		if got := s.safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q", in, got)
		}
	}
}
