package gdoc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocID(t *testing.T) {
	const id = "1K8pItLsspzlFrVQ_XeNruzO79S1kRHtI1v-CduJV388"
	for _, link := range []string{
		"https://docs.google.com/document/d/" + id + "/edit?usp=sharing",
		"https://docs.google.com/document/u/0/d/" + id + "/view",
		"  " + id + " ",
	} {
		if got, err := DocID(link); err != nil || got != id {
			t.Errorf("DocID(%q) = %q, %v", link, got, err)
		}
	}
	if _, err := DocID("https://example.com/whatever"); err == nil {
		t.Error("expected an error for a non-Docs link")
	}
}

func TestFetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "private") {
			// What Google does for a doc that isn't link-shared: a
			// sign-in page with status 200.
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>Sign in</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		w.Header().Set("Content-Disposition", `attachment; filename="My Doc.docx"; filename*=UTF-8''My%20Doc.docx`)
		w.Write([]byte("PK fake"))
	}))
	defer ts.Close()
	old := ExportURL
	ExportURL = func(id string) string { return ts.URL + "/" + id }
	defer func() { ExportURL = old }()

	data, title, err := Fetch(context.Background(), "public")
	if err != nil || string(data) != "PK fake" || title != "My Doc" {
		t.Errorf("Fetch = %q %q %v", data, title, err)
	}
	if _, _, err := Fetch(context.Background(), "private"); err == nil || !strings.Contains(err.Error(), "Anyone with the link") {
		t.Errorf("private doc error = %v", err)
	}
}
