# bedit

A Google Docs-style document viewer, and later a collaborative editor, that
runs behind [bserver](https://github.com/stgnet/bserver).

Documents live at `/bedit/<name>`, and that name is also the document's
directory on disk. Opening the URL shows a live HTML rendering of the
document. `/bedit/` lists the documents you can see and holds the import
form.

## Status: step one

- **Import from Google Docs.** Paste a share link (the doc must be shared as
  "Anyone with the link can view"), or upload a `.docx` file, which covers
  private docs and Word files.
- **Live HTML view** aiming for fidelity with Google Docs: named styles
  (Title, Subtitle, Heading 1–6, Normal text), fonts (loaded from Google
  Fonts), sizes, colors, bold/italic/underline/strike, super/subscript,
  highlight, alignment, indents, paragraph and line spacing, bulleted and
  numbered lists (multi-level, with Docs' numbering styles), links, internal
  links to headings, tabs, page breaks, tables (merged cells, shading,
  borders), and images: inline, cropped, and wrapped with text flowing
  around them.
- **Shared top menu** on every page. Edit, Download and Share are shown
  but disabled until those steps land. Owners get **Re-import**, which
  fetches the latest version from Google, or re-reads an uploaded original
  so improvements to the importer apply to existing docs.
- **Password-less log-in (stub).** You enter a name and a random id is stored
  in a cookie. Whoever imports a doc owns it. Each doc has a public access
  level: private, invite only, view, suggest, or edit. Only view is enforced
  for now, since there is nothing to suggest or edit yet.

Known gaps: Google's checklist bullets export as plain bullets, headers and
footers and footnotes aren't shown yet, and drawings, charts and equations
are skipped.

## Running

```sh
go build
cd /path/to/www/example.com/bedit   # the storage root, writable by bserver's user
/path/to/bedit                      # listens on 127.0.0.1:8090, serves /bedit/
```

| flag | env | default | |
|---|---|---|---|
| `-listen` | `BEDIT_LISTEN` | `127.0.0.1:8090` | TCP address |
| `-socket` | `BEDIT_SOCKET` | | unix socket, replaces `-listen` |
| `-root` | `BEDIT_ROOT` | `.` | storage root |
| `-prefix` | `BEDIT_PREFIX` | `/bedit` | URL path bedit is mounted at |

### Behind bserver

For now bserver forwards to bedit through its existing per-site proxy-path
setting, in the site's `_config.yaml`:

```yaml
proxy-path: /bedit
proxy-path-backend: http://127.0.0.1:8090
proxy-path-allow-private: true
```

bserver's default `max-body-size` (10 MB) also caps `.docx` uploads, so
raise it if you import photo-heavy docs by upload. Link imports aren't
affected, because bedit downloads those from Google itself.

The plan is for bserver to start bedit on demand over a unix socket
(`BEDIT_SOCKET`), so it doesn't have to run all the time. bedit is already
built for that: every bit of state is on disk, so it can stop and restart
at any time.

## Storage

```
<root>/
  <name>/               one directory per document
    meta.json           title, owners, editors, public access, source
    doc.json            current document
    history.jsonl       append-only change log
    img/<hash>.<ext>    images, named by content hash
    src/<seq>.docx      every imported original, verbatim
  _users/<id>.json      users
```

Names starting with `_` are reserved for bedit's own use, so they never
collide with a document.

History is write-once. `history.jsonl` is only ever appended to, and image
and source files are never overwritten. Each import is one entry holding the
complete document. Editing will add entries holding small changes, so
replaying the log up to any entry gives the document as it was at that
point, and `doc.json` is just the log replayed to the end.

`doc.json` is a ProseMirror-style JSON tree (`type`/`attrs`/`content`/`text`/
`marks`) with page geometry, named styles and list definitions stored
alongside it. Using ProseMirror's shape means the planned browser editor can
load it directly, and its change "steps" can go straight into the history
log. See `internal/model` for the full schema. `/bedit/<name>/doc.json`
serves it, which helps when checking what an import produced.

## Why .docx is the import format

Google's `.docx` export (the same file you get from File › Download ›
Microsoft Word) is the only anonymous export that keeps everything in one
file: named styles, fonts, list definitions, the images, and how each image
is anchored and wrapped. The HTML export flattens styles and drops text
wrap, and the Docs API would need OAuth credentials. As a bonus, Word files
import through the same code.

## Layout

```
main.go              flags, listener
internal/model       the document format (doc.json)
internal/docx        .docx -> model
internal/gdoc        fetch a Google Doc as .docx from its link
internal/render      model -> HTML/CSS
internal/store       files on disk, history, users, access rules
internal/server      HTTP handlers, templates, menu
internal/docxtest    builds small .docx files for tests
```
