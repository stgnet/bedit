// Package store owns bedit's files on disk.
//
// Layout, relative to the storage root (the site's bedit/ directory, which
// is also bedit's working directory when bserver runs it):
//
//	<name>/                 one directory per document; <name> is the URL
//	  meta.json             title, owners, editors, public access, source
//	  doc.json              current document (model.Document)
//	  history.jsonl         append-only change log, one JSON entry per line
//	  img/<hash>.<ext>      images, named by content hash, never rewritten
//	  src/<seq>.docx        each imported original, kept verbatim
//	_users/<id>.json        one file per (password-less) user
//
// Names starting with "_" are reserved for bedit itself, so they can never
// collide with a document name or a URL route.
//
// Write-once-read-many: history.jsonl is only ever opened for append, and
// image and source files are created with O_EXCL so existing bytes are never
// replaced. doc.json is a materialized copy of "history replayed to the
// end"; it can always be rebuilt from history, which is what makes
// rewinding to any earlier point possible. meta.json is ordinary mutable
// state.
package store

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stgnet/bedit/internal/model"
)

// Public access levels for a document, as chosen by its owners.
const (
	PublicPrivate = "private" // only owners and editors; not listed for anyone else
	PublicNone    = "none"    // invite only: listed, but content needs an invite
	PublicView    = "view"    // anyone can read
	PublicSuggest = "suggest" // anyone can read and (later) suggest changes
	PublicEdit    = "edit"    // anyone can read and (later) edit
)

// PublicLevels lists the levels in the order the UI offers them.
var PublicLevels = []string{PublicPrivate, PublicNone, PublicView, PublicSuggest, PublicEdit}

// Meta is the content of meta.json.
type Meta struct {
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Created time.Time `json:"created"`
	Owners  []string  `json:"owners"`  // user ids; may grant and revoke access
	Editors []string  `json:"editors"` // user ids; may edit only
	Public  string    `json:"public"`
	Source  Source    `json:"source"`
}

// Source records where a document was imported from, so it can be
// refreshed later.
type Source struct {
	Kind     string `json:"kind"`               // "gdoc" or "upload"
	URL      string `json:"url,omitempty"`      // the link the user gave
	DocID    string `json:"docId,omitempty"`    // Google Doc id
	Filename string `json:"filename,omitempty"` // uploaded file name
}

// Entry is one line of history.jsonl.
//
// Only "import" exists so far: it carries a complete document, replacing
// whatever came before. Editing will add entries carrying ProseMirror steps
// against the previous state; replaying entries 1..N gives the document as
// it was at entry N.
type Entry struct {
	Seq      int             `json:"seq"`
	Time     time.Time       `json:"time"`
	User     string          `json:"user"`
	Op       string          `json:"op"`
	Source   string          `json:"source,omitempty"`   // where an import came from
	Original string          `json:"original,omitempty"` // preserved source file, e.g. src/1.docx
	Doc      *model.Document `json:"doc,omitempty"`      // full document, for imports
}

// User is a password-less user, identified by a random id held in a cookie.
type User struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

// Store is the storage root.
type Store struct {
	Root string

	// One lock per document serializes writers within this process.
	// bedit runs as a single process per site, so nothing more is needed.
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns a Store rooted at dir.
func New(dir string) *Store {
	return &Store{Root: dir, locks: map[string]*sync.Mutex{}}
}

func (s *Store) lock(name string) func() {
	s.mu.Lock()
	l, ok := s.locks[name]
	if !ok {
		l = &sync.Mutex{}
		s.locks[name] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// nameRE is what a document name may look like. Lower case only, so two
// names never differ just by case (which would clash on case-insensitive
// filesystems), and nothing that needs escaping in a URL or a path.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

// ValidName reports whether name can be used as a document name.
func ValidName(name string) bool {
	return nameRE.MatchString(name)
}

// Slug suggests a document name from a title: "My Trip (2024)" -> "my-trip-2024".
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return s
}

// RandomName returns a short random name, for titles with no usable letters.
func RandomName() string {
	return RandomID(5)
}

// RandomID returns n random bytes as hex.
func RandomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the system is unusable
	}
	return hex.EncodeToString(b)
}

func (s *Store) dir(name string) string {
	return filepath.Join(s.Root, name)
}

// Exists reports whether a document with this name exists.
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(s.dir(name), "meta.json"))
	return err == nil
}

// ErrExists is returned by Create when the name is taken.
var ErrExists = errors.New("a document with that name already exists")

// Create makes a new document directory from an import. If name is taken
// and pickFree is set, -2, -3, ... is appended until a free name is found;
// otherwise ErrExists is returned. It returns the name actually used.
func (s *Store) Create(name string, pickFree bool, meta *Meta, doc *model.Document, images map[string][]byte, original []byte, user, source string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid document name %q", name)
	}
	// os.Mkdir is the atomic "claim this name" step: it fails if anything
	// at all is already there (a document, or any other file in the site
	// directory, such as the bedit binary itself).
	base := name
	for i := 2; ; i++ {
		err := os.Mkdir(s.dir(name), 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		if !pickFree {
			return "", ErrExists
		}
		if i > 1000 {
			return "", ErrExists
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	meta.Name = name
	err := s.writeMeta(meta)
	if err == nil {
		err = s.Import(name, doc, images, original, user, source)
	}
	if err != nil {
		// We created this directory a moment ago, so removing it only
		// discards our own half-finished work and frees the name.
		os.RemoveAll(s.dir(name))
		return "", err
	}
	return name, nil
}

// Import records a (re-)import of a document: stores its images and
// original file, appends a history entry, and makes it the current doc.
func (s *Store) Import(name string, doc *model.Document, images map[string][]byte, original []byte, user, source string) error {
	defer s.lock(name)()
	dir := s.dir(name)
	for file, data := range images {
		if err := writeOnce(filepath.Join(dir, "img", file), data); err != nil {
			return err
		}
	}
	seq, err := s.nextSeq(name)
	if err != nil {
		return err
	}
	e := &Entry{Seq: seq, Time: time.Now().UTC(), User: user, Op: "import", Source: source, Doc: doc}
	if original != nil {
		e.Original = fmt.Sprintf("src/%d.docx", seq)
		if err := writeOnce(filepath.Join(dir, e.Original), original); err != nil {
			return err
		}
	}
	if err := s.appendHistory(name, e); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "doc.json"), doc)
}

// writeOnce creates a file that must not already exist with different
// content. Content-addressed files (images) that already exist are fine:
// same name means same bytes.
func writeOnce(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// nextSeq counts history entries. Reading the whole log is fine at this
// stage (imports are rare); editing will keep the count in memory.
func (s *Store) nextSeq(name string) (int, error) {
	f, err := os.Open(filepath.Join(s.dir(name), "history.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadSlice('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			n++
		}
		if err == bufio.ErrBufferFull {
			// A long line (a full document) spans several reads;
			// keep reading until its newline.
			continue
		}
		if err != nil {
			break
		}
	}
	return n + 1, nil
}

func (s *Store) appendHistory(name string, e *Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// O_APPEND without O_TRUNC: the log can only grow. The single write
	// call puts the whole entry in at once, and Sync makes sure it is on
	// disk before doc.json claims it happened.
	f, err := os.OpenFile(filepath.Join(s.dir(name), "history.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeJSON replaces a file atomically (write a temp file, then rename),
// so a reader never sees a half-written doc.json or meta.json.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (s *Store) writeMeta(m *Meta) error {
	return writeJSON(filepath.Join(s.dir(m.Name), "meta.json"), m)
}

// SaveMeta replaces a document's meta.json.
func (s *Store) SaveMeta(m *Meta) error {
	defer s.lock(m.Name)()
	return s.writeMeta(m)
}

// Meta loads a document's meta.json.
func (s *Store) Meta(name string) (*Meta, error) {
	if !ValidName(name) {
		return nil, fs.ErrNotExist
	}
	m := &Meta{}
	if err := readJSON(filepath.Join(s.dir(name), "meta.json"), m); err != nil {
		return nil, err
	}
	m.Name = name
	return m, nil
}

// Doc loads a document's current content.
func (s *Store) Doc(name string) (*model.Document, error) {
	if !ValidName(name) {
		return nil, fs.ErrNotExist
	}
	d := &model.Document{}
	return d, readJSON(filepath.Join(s.dir(name), "doc.json"), d)
}

// ImagePath returns the on-disk path of a document image, or "" if file is
// not a plausible image name (which also rules out path traversal).
func (s *Store) ImagePath(name, file string) string {
	if !ValidName(name) || !imgRE.MatchString(file) {
		return ""
	}
	return filepath.Join(s.dir(name), "img", file)
}

var imgRE = regexp.MustCompile(`^[a-f0-9]{8,64}\.(png|jpg|gif|webp|bmp)$`)

// List returns the metadata of every document, newest first.
func (s *Store) List() ([]*Meta, error) {
	ents, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	var out []*Meta
	for _, e := range ents {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		if m, err := s.Meta(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Users

var userRE = regexp.MustCompile(`^[a-f0-9]{32}$`)

// NewUser creates a user with a fresh random id.
func (s *Store) NewUser(name string) (*User, error) {
	u := &User{ID: RandomID(16), Name: name, Created: time.Now().UTC()}
	return u, writeJSON(filepath.Join(s.Root, "_users", u.ID+".json"), u)
}

// User loads a user by id; nil if there is no such user.
func (s *Store) User(id string) *User {
	if !userRE.MatchString(id) {
		return nil
	}
	u := &User{}
	if err := readJSON(filepath.Join(s.Root, "_users", id+".json"), u); err != nil {
		return nil
	}
	return u
}

// ---------------------------------------------------------------------------
// Access rules

// Role returns the user's role on a document: "owner", "editor" or "".
func (m *Meta) Role(userID string) string {
	if userID == "" {
		return ""
	}
	for _, id := range m.Owners {
		if id == userID {
			return "owner"
		}
	}
	for _, id := range m.Editors {
		if id == userID {
			return "editor"
		}
	}
	return ""
}

// CanView reports whether a user (or "" for anonymous) may read the doc.
func (m *Meta) CanView(userID string) bool {
	if m.Role(userID) != "" {
		return true
	}
	switch m.Public {
	case PublicView, PublicSuggest, PublicEdit:
		return true
	}
	return false
}

// Listed reports whether the doc should appear in this user's document
// list. Invite-only docs are listed so people can ask for access; private
// ones are invisible to everyone but their owners and editors.
func (m *Meta) Listed(userID string) bool {
	return m.CanView(userID) || m.Public == PublicNone
}

// LatestOriginal returns the most recently preserved import source file.
func (s *Store) LatestOriginal(name string) ([]byte, error) {
	if !ValidName(name) {
		return nil, fs.ErrNotExist
	}
	ents, err := os.ReadDir(filepath.Join(s.dir(name), "src"))
	if err != nil {
		return nil, errors.New("no original file is stored for this document")
	}
	best, bestSeq := "", 0
	for _, e := range ents {
		var seq int
		if _, err := fmt.Sscanf(e.Name(), "%d.docx", &seq); err == nil && seq > bestSeq {
			best, bestSeq = e.Name(), seq
		}
	}
	if best == "" {
		return nil, errors.New("no original file is stored for this document")
	}
	return os.ReadFile(filepath.Join(s.dir(name), "src", best))
}
