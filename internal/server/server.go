// Package server is bedit's web front end: the /bedit index with its import
// form and document list, the live document view at /bedit/<name>, and the
// stub log-in.
//
// Every route lives under a configurable prefix ("/bedit" by default)
// because bserver forwards the path unchanged. Route names that are not
// documents start with "_", which document names can never do, so the two
// can't collide.
package server

import (
	"embed"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stgnet/bedit/internal/docx"
	"github.com/stgnet/bedit/internal/gdoc"
	"github.com/stgnet/bedit/internal/render"
	"github.com/stgnet/bedit/internal/store"
)

//go:embed web
var webFS embed.FS

const cookieName = "bedit_user"

// Server holds what the handlers share.
type Server struct {
	store  *store.Store
	prefix string // e.g. "/bedit", never with a trailing slash
	pages  map[string]*template.Template
}

// New creates a Server for the store, serving under prefix.
func New(st *store.Store, prefix string) *Server {
	s := &Server{store: st, prefix: strings.TrimRight(prefix, "/"), pages: map[string]*template.Template{}}
	funcs := template.FuncMap{"publicLabel": publicLabel}
	// Each page is parsed together with the shared base layout (which
	// holds the menu), so every page gets the same menu.
	for _, p := range []string{"index", "doc", "login", "logout", "error"} {
		s.pages[p] = template.Must(template.New("base.html").Funcs(funcs).ParseFS(webFS, "web/base.html", "web/"+p+".html"))
	}
	return s
}

// Handler returns the HTTP handler with all routes.
func (s *Server) Handler() http.Handler {
	p := s.prefix
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+p+"/{$}", s.index)
	mux.HandleFunc("GET "+p+"/_static/bedit.css", s.css)
	mux.HandleFunc("GET "+p+"/_login", s.loginForm)
	mux.HandleFunc("POST "+p+"/_login", s.login)
	mux.HandleFunc("GET "+p+"/_logout", s.logoutForm)
	mux.HandleFunc("POST "+p+"/_logout", s.logout)
	mux.HandleFunc("POST "+p+"/_import", s.importDoc)
	mux.HandleFunc("GET "+p+"/{name}", s.viewDoc)
	mux.HandleFunc("GET "+p+"/{name}/{$}", s.slashRedirect)
	mux.HandleFunc("GET "+p+"/{name}/doc.json", s.docJSON)
	mux.HandleFunc("GET "+p+"/{name}/img/{file}", s.image)
	mux.HandleFunc("POST "+p+"/{name}/_reimport", s.reimport)
	if p != "" {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, p+"/", http.StatusMovedPermanently)
		})
	}
	return securityHeaders(mux)
}

// securityHeaders adds headers that limit the damage a malicious document
// could do. bedit serves no JavaScript yet, so scripts are refused
// outright; the editor step will relax this to its own script files.
// Fonts come from Google Fonts, which Docs' font menu is built on.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; img-src 'self' data:; "+
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; "+
			"form-action 'self'; frame-ancestors 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Page rendering

// page is the data every template gets. Fields a page doesn't use are
// left empty.
type page struct {
	Prefix string
	User   *store.User
	Title  string // <title>
	Next   string // where to return after logging in

	// document view
	Meta *store.Meta
	Role string
	Doc  *render.Page

	// index
	Docs    []docRow
	Levels  []string
	Form    importForm
	Error   string
	Message string
}

type docRow struct {
	Meta    *store.Meta
	Role    string
	CanView bool
	Owner   string
}

type importForm struct {
	Link, Name, Public string
}

func (s *Server) newPage(r *http.Request, title string) *page {
	return &page{Prefix: s.prefix, User: s.user(r), Title: title, Next: r.URL.Path}
}

func (s *Server) show(w http.ResponseWriter, status int, name string, p *page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Pages depend on who is logged in and on documents that change, so
	// browsers and proxies must not reuse them.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages[name].Execute(w, p); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	p := s.newPage(r, http.StatusText(status))
	p.Error = msg
	s.show(w, status, "error", p)
}

func publicLabel(level string) string {
	switch level {
	case store.PublicPrivate:
		return "Private (owners and editors only)"
	case store.PublicNone:
		return "Invite only"
	case store.PublicView:
		return "Anyone can view"
	case store.PublicSuggest:
		return "Anyone can suggest changes"
	case store.PublicEdit:
		return "Anyone can edit"
	}
	return level
}

func (s *Server) css(w http.ResponseWriter, r *http.Request) {
	data, _ := webFS.ReadFile("web/bedit.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
}

// ---------------------------------------------------------------------------
// Users (stub log-in)
//
// There are no passwords: logging in just creates a user with a display
// name and a long random id, and the id lives in a cookie. Knowing the id
// is what proves who you are, so it is unguessable (128 bits) and
// HttpOnly. SameSite=Lax keeps other sites from making form posts that
// carry it.

func (s *Server) user(r *http.Request) *store.User {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	return s.store.User(c.Value)
}

func (s *Server) userID(r *http.Request) string {
	if u := s.user(r); u != nil {
		return u.ID
	}
	return ""
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     s.prefix + "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// safeNext only allows returning to a page of our own after log-in, never
// to another site.
func (s *Server) safeNext(next string) string {
	if strings.HasPrefix(next, s.prefix+"/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return s.prefix + "/"
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(r, "Log in")
	p.Next = s.safeNext(r.URL.Query().Get("next"))
	s.show(w, http.StatusOK, "login", p)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	next := s.safeNext(r.FormValue("next"))
	if name == "" || len(name) > 60 {
		p := s.newPage(r, "Log in")
		p.Next, p.Error = next, "Please enter a name of up to 60 characters."
		s.show(w, http.StatusBadRequest, "login", p)
		return
	}
	u, err := s.store.NewUser(name)
	if err != nil {
		log.Printf("new user: %v", err)
		s.fail(w, r, http.StatusInternalServerError, "Couldn't create the user.")
		return
	}
	s.setCookie(w, r, u.ID, 10*365*24*3600)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// logoutForm asks for confirmation, because with no password a user who
// logs out can't get back in as the same user (and so loses ownership of
// their documents) until real log-in exists.
func (s *Server) logoutForm(w http.ResponseWriter, r *http.Request) {
	s.show(w, http.StatusOK, "logout", s.newPage(r, "Log out"))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, "", -1)
	http.Redirect(w, r, s.prefix+"/", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Index: document list and import form

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.showIndex(w, r, http.StatusOK, importForm{Public: store.PublicPrivate}, "")
}

func (s *Server) showIndex(w http.ResponseWriter, r *http.Request, status int, form importForm, errMsg string) {
	p := s.newPage(r, "bedit")
	p.Form, p.Error, p.Levels = form, errMsg, store.PublicLevels
	uid := s.userID(r)
	metas, err := s.store.List()
	if err != nil {
		log.Printf("list: %v", err)
	}
	names := map[string]string{} // owner id -> display name, looked up once
	for _, m := range metas {
		if !m.Listed(uid) {
			continue
		}
		row := docRow{Meta: m, Role: m.Role(uid), CanView: m.CanView(uid)}
		if len(m.Owners) > 0 {
			id := m.Owners[0]
			if _, ok := names[id]; !ok {
				if u := s.store.User(id); u != nil {
					names[id] = u.Name
				}
			}
			row.Owner = names[id]
		}
		p.Docs = append(p.Docs, row)
	}
	s.show(w, status, "index", p)
}

func (s *Server) importDoc(w http.ResponseWriter, r *http.Request) {
	u := s.user(r)
	if u == nil {
		http.Redirect(w, r, s.prefix+"/_login?next="+s.prefix+"/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, gdoc.MaxSize+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.showIndex(w, r, http.StatusBadRequest, importForm{Public: store.PublicPrivate}, "The upload failed or was too large.")
		return
	}
	form := importForm{
		Link:   strings.TrimSpace(r.FormValue("link")),
		Name:   strings.ToLower(strings.TrimSpace(r.FormValue("name"))),
		Public: r.FormValue("public"),
	}
	bad := func(msg string) { s.showIndex(w, r, http.StatusBadRequest, form, msg) }
	if !validPublic(form.Public) {
		form.Public = store.PublicPrivate
	}
	if form.Name != "" && !store.ValidName(form.Name) {
		bad("Names may use lower-case letters, digits and dashes, and must start with a letter or digit.")
		return
	}

	// A file upload wins over a link if both were given.
	var data []byte
	var title string
	var src store.Source
	if f, hdr, err := r.FormFile("file"); err == nil && hdr.Size > 0 {
		defer f.Close()
		if data, err = io.ReadAll(f); err != nil {
			bad("Couldn't read the uploaded file.")
			return
		}
		title = gdoc.TitleFromFilename(hdr.Filename)
		src = store.Source{Kind: "upload", Filename: filepath.Base(hdr.Filename)}
	} else if form.Link != "" {
		id, err := gdoc.DocID(form.Link)
		if err != nil {
			bad(err.Error())
			return
		}
		if data, title, err = gdoc.Fetch(r.Context(), id); err != nil {
			bad(err.Error())
			return
		}
		src = store.Source{Kind: "gdoc", URL: form.Link, DocID: id}
	} else {
		bad("Paste a Google Docs link, or choose a .docx file to upload.")
		return
	}

	res, err := docx.Import(data, title)
	if err != nil {
		bad("Couldn't read that document: " + err.Error())
		return
	}

	// Use the name the user picked, or suggest one from the title. Only a
	// suggested name gets -2, -3... added when taken; a name the user
	// chose on purpose is reported as taken instead.
	name, pickFree := form.Name, false
	if name == "" {
		name, pickFree = store.Slug(res.Doc.Title), true
		if name == "" {
			name = store.RandomName()
		}
	}
	meta := &store.Meta{
		Title:   res.Doc.Title,
		Created: time.Now().UTC(),
		Owners:  []string{u.ID},
		Editors: []string{},
		Public:  form.Public,
		Source:  src,
	}
	name, err = s.store.Create(name, pickFree, meta, res.Doc, res.Images, data, u.ID, sourceLabel(src))
	if errors.Is(err, store.ErrExists) {
		bad("A document named \"" + form.Name + "\" already exists. Pick another name, or leave it blank for a suggested one.")
		return
	}
	if err != nil {
		log.Printf("create %s: %v", name, err)
		bad("Couldn't save the document.")
		return
	}
	http.Redirect(w, r, s.prefix+"/"+name, http.StatusSeeOther)
}

func validPublic(level string) bool {
	for _, l := range store.PublicLevels {
		if l == level {
			return true
		}
	}
	return false
}

// sourceLabel is how a source is recorded in the history log.
func sourceLabel(src store.Source) string {
	if src.Kind == "gdoc" {
		return "gdoc:" + src.DocID
	}
	return "upload:" + src.Filename
}

// ---------------------------------------------------------------------------
// Document view

// docAccess loads a document's meta and checks the current user may view
// it, writing the error page and returning nil if not.
func (s *Server) docAccess(w http.ResponseWriter, r *http.Request) *store.Meta {
	m, err := s.store.Meta(r.PathValue("name"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "There's no document here.")
		return nil
	}
	if !m.CanView(s.userID(r)) {
		msg := "This document is shared only with specific people."
		if s.user(r) == nil {
			msg += " If you have access, log in first."
		} else {
			msg += " Ask an owner for access."
		}
		s.fail(w, r, http.StatusForbidden, msg)
		return nil
	}
	return m
}

func (s *Server) viewDoc(w http.ResponseWriter, r *http.Request) {
	m := s.docAccess(w, r)
	if m == nil {
		return
	}
	doc, err := s.store.Doc(m.Name)
	if err != nil {
		log.Printf("load %s: %v", m.Name, err)
		s.fail(w, r, http.StatusInternalServerError, "Couldn't load the document.")
		return
	}
	p := s.newPage(r, m.Title)
	p.Meta, p.Role = m, m.Role(p.userID())
	p.Doc = render.Render(doc, s.prefix+"/"+m.Name+"/")
	s.show(w, http.StatusOK, "doc", p)
}

func (p *page) userID() string {
	if p.User != nil {
		return p.User.ID
	}
	return ""
}

// slashRedirect sends /bedit/name/ to /bedit/name, so each document has
// exactly one URL.
func (s *Server) slashRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.prefix+"/"+r.PathValue("name"), http.StatusMovedPermanently)
}

// docJSON serves the stored document model, handy for checking what an
// import produced.
func (s *Server) docJSON(w http.ResponseWriter, r *http.Request) {
	m := s.docAccess(w, r)
	if m == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(s.store.Root, m.Name, "doc.json"))
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	m := s.docAccess(w, r)
	if m == nil {
		return
	}
	path := s.store.ImagePath(m.Name, r.PathValue("file"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	// Image names are content hashes, so a given URL's bytes never
	// change and browsers may cache them indefinitely. "private"
	// because access depends on who is asking.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

// reimport runs the import again: from Google for a Google Doc (picking up
// changes made there), or from the preserved original for an upload
// (picking up improvements to the importer). Owners only.
func (s *Server) reimport(w http.ResponseWriter, r *http.Request) {
	m := s.docAccess(w, r)
	if m == nil {
		return
	}
	u := s.user(r)
	if u == nil || m.Role(u.ID) != "owner" {
		s.fail(w, r, http.StatusForbidden, "Only an owner can re-import a document.")
		return
	}
	var data []byte
	var title string
	var err error
	if m.Source.Kind == "gdoc" {
		data, title, err = gdoc.Fetch(r.Context(), m.Source.DocID)
	} else {
		data, err = s.store.LatestOriginal(m.Name)
		title = m.Title
	}
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	res, err := docx.Import(data, title)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "Couldn't read that document: "+err.Error())
		return
	}
	// Only keep a new copy of the original when it actually came from
	// somewhere new; re-reading our own preserved file adds nothing.
	original := data
	if m.Source.Kind != "gdoc" {
		original = nil
	}
	if err := s.store.Import(m.Name, res.Doc, res.Images, original, u.ID, sourceLabel(m.Source)); err != nil {
		log.Printf("reimport %s: %v", m.Name, err)
		s.fail(w, r, http.StatusInternalServerError, "Couldn't save the document.")
		return
	}
	if m.Title != res.Doc.Title {
		m.Title = res.Doc.Title
		if err := s.store.SaveMeta(m); err != nil {
			log.Printf("save meta %s: %v", m.Name, err)
		}
	}
	http.Redirect(w, r, s.prefix+"/"+m.Name, http.StatusSeeOther)
}
