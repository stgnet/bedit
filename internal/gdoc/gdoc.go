// Package gdoc fetches a Google Doc as a .docx file from its share link.
//
// It uses the same export URL as Docs' File > Download > Microsoft Word,
// which works without any Google credentials as long as the doc is shared
// as "Anyone with the link can view". That keeps bedit free of OAuth setup;
// private docs can still be imported by downloading the .docx and
// uploading it.
package gdoc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// MaxSize caps how much we will download. Docs with many photos export
// large; anything past this is far more likely to be a mistake.
const MaxSize = 200 << 20

// idRE pulls the document id out of the usual link shapes:
// https://docs.google.com/document/d/<id>/edit?usp=sharing, /d/<id>/view,
// /document/u/0/d/<id>/..., or a bare id.
var (
	linkRE = regexp.MustCompile(`/document/(?:u/\d+/)?d/([A-Za-z0-9_-]{20,})`)
	bareRE = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

// DocID returns the Google Doc id in a share link (or a bare id).
func DocID(link string) (string, error) {
	link = strings.TrimSpace(link)
	if m := linkRE.FindStringSubmatch(link); m != nil {
		return m[1], nil
	}
	if bareRE.MatchString(link) {
		return link, nil
	}
	return "", errors.New("that doesn't look like a Google Docs link (expected https://docs.google.com/document/d/...)")
}

// ExportURL is where the .docx export of a doc lives. A variable so tests
// can point it at a local server.
var ExportURL = func(id string) string {
	return "https://docs.google.com/document/d/" + id + "/export?format=docx"
}

// Fetch downloads the doc as .docx and returns the file and the doc's
// title (taken from the download's file name, which Docs sets to the
// document title).
func Fetch(ctx context.Context, id string) (data []byte, title string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ExportURL(id), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetching the doc from Google: %w", err)
	}
	defer resp.Body.Close()

	// A doc that isn't link-shared doesn't fail with 403: Google redirects
	// to its sign-in page, which ends as a 200 HTML page. So the content
	// type is the real test of success.
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.Contains(ct, "wordprocessingml") {
		if resp.StatusCode == http.StatusNotFound {
			return nil, "", errors.New("Google says that document doesn't exist")
		}
		return nil, "", errors.New("Google wouldn't export that doc. Share it as \"Anyone with the link can view\" and try again, or download it as .docx (File > Download > Microsoft Word) and upload the file instead")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, MaxSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("downloading the doc: %w", err)
	}
	if len(data) > MaxSize {
		return nil, "", fmt.Errorf("that doc is larger than %d MB", MaxSize>>20)
	}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		title = TitleFromFilename(params["filename"])
	}
	return data, title, nil
}

// TitleFromFilename strips the extension from a downloaded or uploaded
// file name to recover the document title.
func TitleFromFilename(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	return name
}
