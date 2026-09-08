# Flow Notes — Local Helper (v0.5.0)

The on-your-machine half of Flow Notes. It handles the two storage targets
the hosted backend cannot reach: **Apple Notes** and **local Markdown
files**. Notes stored in Google Drive don't involve it at all.

Chrome launches it as a **native messaging host** — it starts on demand,
talks over stdin/stdout, and exits when Chrome is done. There is no port,
no server for the user to start, and nothing left running.

Stdlib only, so there is nothing to fetch to build it.

## Installing

```
./install.sh chrome-extension://<your-extension-id>
```

The extension's setup panel shows this command with your id already filled
in. It installs the binary to `~/.local/bin/flow-notes-local` and
registers it with every Chromium-family browser it finds (Chrome, Chrome
Beta/Canary, Chromium, Brave, Edge, Arc). **Restart Chrome afterwards.**

`./install.sh --uninstall` removes the binary and the registrations,
leaving your notes alone.

Two things worth knowing:

- **The install path is fixed on purpose.** macOS ties the Notes
  automation permission to the binary's location, so installing it
  somewhere else means granting permission again.
- **The permission dialog names Google Chrome**, not this helper, because
  macOS attributes automation to whatever launched the process — and
  that's Chrome.

## How the extension talks to it

Length-prefixed JSON on stdin/stdout: a 4-byte native-endian length, then
that many bytes of UTF-8 JSON.

```json
→ {"id": 7, "method": "note.create", "params": {"title": "Reading list"}}
← {"id": 7, "ok": true, "path": "/Users/you/FlowNotes/reading-list.md"}
```

Failures come back **in band**, so one bad call never takes down the
channel:

```json
← {"id": 8, "ok": false, "code": "unsupported", "error": "Apple Notes is only available on macOS"}
```

`code` is one of `invalid`, `unsupported` (platform limit), `forbidden`
(macOS withheld automation permission) or `failed`, so the extension does
not have to guess a cause from prose.

| Method | Params | Returns |
| --- | --- | --- |
| `health` | — | `version`, `dir`, `apple_notes` |
| `note.create` | `title` | `path`, `name` |
| `note.append` | `path`, `text`, `source_url`, `source_title` | `ok` |
| `note.write` | `path`, `content` | `ok`, `backup` |
| `note.read` | `path` | `content` |
| `note.delete` | `path` | `trashed` |
| `apple.create` | `title` | `id` |
| `apple.append` | `id`, `text`, `source_url`, `source_title` | `ok` |
| `apple.write` | `id`, `html` | `ok` |
| `apple.read` | `id` | `html` |
| `apple.delete` | `id` | `ok` |
| `apple.open` | `id` | `ok` |

**Everything diagnostic goes to stderr.** Stdout is the wire; a stray byte
there corrupts the framing and hangs the channel.

Chrome's size limits are 64 MiB for a message it sends and 1 MB for one it
receives. Note content travels in the generous direction; replies here are
small. An over-sized reply is replaced with an error rather than being
dropped silently by Chrome.

### Development mode

```
go run . -serve -origin chrome-extension://<id>
```

Runs an HTTP server on `127.0.0.1:4500` exposing the same methods, so the
helper can be driven with `curl`. That is how most of this project's
integration bugs were found, and it is the reason the HTTP transport still
exists. Chrome never uses it.

Both transports dispatch into the same `API` (`api.go`), so they cannot
drift apart as methods are added.

### Why it reads as well as writes

The server keeps no copy of anything captured, so a note's content lives
only in the note. Organizing therefore has to read the only copy, and for
Apple Notes and local files that is this process.

`apple.read` returns `body` (HTML), never `plaintext`: plaintext drops the
href from every `[source]` link, so a note organized twice would lose all
its citations.

## Storage targets

### Apple Notes (macOS)

Notes are created in, appended to, opened and rewritten in Notes.app.
Clicking a note in the extension brings it up in Notes — the reason this
target exists at all.

Notes.app stores bodies as **HTML**, which suits this project better than
Markdown: the `[source]` marker is a real `<a href>`, so an Apple note
behaves exactly like the Google Docs version.

Every operation runs through `osascript` using AppleScript's `on run argv`
form, with data passed as process arguments. **Nothing is interpolated
into script source** — capture text comes from arbitrary web pages, and
building scripts by concatenation would make that text executable. Page
text is HTML-escaped before it reaches a note body.

Deleting sends a note to Notes' *Recently Deleted*, recoverable for 30
days.

Not macOS? Nothing breaks: the Markdown target is unaffected and the Apple
methods return `code: "unsupported"` with a message the extension shows
verbatim. The extension also removes the Apple Notes option from its
picker on other platforms, so the error is a backstop rather than
something a user would normally hit.

### Local Markdown files

`~/FlowNotes` by default (`-dir`). Filenames come from note titles,
de-duplicated:

```markdown
# Reading list

Attention is all you need 🎉

[source](https://arxiv.org/abs/1706.03762)
```

`note.write` (used by the organize pass) copies the previous contents into
`.trash` first — organizing rewrites the whole note from the capture log,
so anything typed in by hand would otherwise vanish silently. Deletes move
the file to `.trash` too; nothing here calls `os.Remove` on a note.

## Security

**Containment is the boundary.** Every path in a request is validated to
name an existing regular `.md` file whose *resolved parent* is the notes
directory. The check compares resolved paths, not string prefixes, so
neither `../` segments nor a symlink planted in the notes directory can
redirect a write. `store_test.go` covers absolute paths elsewhere,
traversal, symlinked notes and non-Markdown files, for append, write and
delete. Removing the check makes those tests fail — verified by mutation.

**Access control is Chrome's.** The host manifest lists exactly one
`allowed_origins` entry, so only your extension can launch the helper.
That replaces the CORS and Private Network Access handling the HTTP
transport needed. (`-serve` mode keeps an `-origin` check for development.)

Neither defends against another program already running as you on the same
machine. That is an accepted limit, not an oversight.

## Cutting a release

```
./make-release.sh
```

Builds macOS `arm64` and `amd64` binaries, bundles them with `install.sh`,
this README and a plain-language `SETUP.txt`, and zips it into `dist/`.
Upload that to GitHub Releases and point `HELPER_DOWNLOAD_URL` in the
extension's `background.js` at it.

`install.sh` uses a bundled binary when it finds one and builds from source
otherwise, so one script serves both a downloaded release (no Go needed)
and a source checkout.

**The binaries are unsigned.** macOS quarantines anything downloaded and
refuses to run it, saying the developer can't be verified. The installer
clears that flag and `SETUP.txt` explains what to expect. Removing the
warning entirely means the Apple Developer Program ($99/yr) plus signing
and notarization.

### Version handshake

`health` reports `version`. The extension compares it against its own
`HELPER_MIN_VERSION` and shows an update prompt if the helper is older.
**Bump `Version` in `main.go` whenever the method set changes**, and raise
`HELPER_MIN_VERSION` in `background.js` to match.

## Conventions

The shell scripts are deliberately **ASCII-only**. Under a UTF-8 locale
`/bin/sh` absorbs a multi-byte character that immediately follows a
variable reference into the variable *name*: `"$PLIST…"` became a lookup
for a variable called `PLIST…`, and with `set -u` that aborted the script.
`sh -n` does not catch it — it is an expansion-time fault, not a syntax
error.
