# Handoff to Claude Code — Flow Notes Project

## What this project is

See `notes-extension-project-brief.md` for the full design spec, decisions,
and reasoning (storage architecture, AI organize modes, source-linking,
local-file mode, etc.) — read that first for context.

## What exists so far

- `flow-notes-extension/` — Chrome extension skeleton (MV3). Content script
  with selection popup, background service worker (message routing + auth
  token handling), toolbar popup UI. See its own README.md for what's real
  vs. stubbed.
- `flow-notes-backend/` — Go backend skeleton. Working: `GET/POST /api/notes`,
  `POST /api/notes/{id}/captures`, Postgres schema. See its own README.md.

## Done since this handoff was written (Claude Code session, 2026-09-07)

The Drive/Docs integration described below is **implemented, building, and
tested**. Implemented as designed — no design changes were needed.

- **`internal/google/docs.go`** (new) — `CreateDoc` and `AppendCapture`,
  plain `net/http` + `encoding/json` + `unicode/utf16`, no new deps.
  `AppendCapture` does `documents.get` → `batchUpdate` with `insertText`
  + `updateTextStyle`, scoping the link to just the `[source]` marker.
  UTF-16 code-unit offsets as flagged. Also: non-`http(s)` source URLs
  skip the marker rather than writing an unsafe link, and a typed
  `*google.APIError` carries the status code so callers can tell 401
  (re-auth) from 403 (scope problem).
- **`internal/google/docs_test.go`** (new) — stub-server tests. The
  UTF-16 test is the important one: it re-derives the link span from the
  inserted text and fails with emoji/CJK content if the offset math is
  replaced with `len()` or a rune count. Verified by mutation.
- **`internal/handlers/auth.go`** — stub replaced with real tokeninfo
  verification. Uses `sub` as the user id, puts the raw token in the
  context under a second key, and optionally enforces `aud` against
  `GOOGLE_OAUTH_CLIENT_ID`. Verified tokens are cached in memory (≤5 min,
  or token expiry) so capture doesn't pay a Google round-trip per
  highlight. `AUTH_DEV_MODE=1` restores the old stub for local curl-ing.
- **`internal/handlers/notes.go`** — `CreateNote` creates the Doc first
  and stores `drive_file_id`; if the Docs call fails no row is written.
- **`internal/handlers/captures.go`** — `AddCapture` pushes to the Doc
  after committing to Postgres, best-effort, surfacing
  `"drive_sync": "ok" | "failed"` in the response as suggested.
- **`background.js`** (extension) — one consequence of real token
  verification: `chrome.identity` hands back cached tokens that may
  already be expired, which now 401 instead of being waved through. Added
  a `removeCachedAuthToken` + single retry on 401, and passed `drive_sync`
  through to callers. No other extension work was done.
- Backend READMEs updated. `go build ./...`, `go vet ./...` and
  `go test ./...` are all clean (Go 1.25.1); `go.sum` now exists.

**Not yet verified end-to-end** — still blocked on a real Postgres
instance and a real Google OAuth client id (`manifest.json`'s is still a
placeholder). The Docs calls are exercised only against a stub server, so
first contact with the real API may still turn up surprises.

## Deployment-readiness pass (same Claude Code session)

**OAuth scopes narrowed — do not re-add `documents`.** `manifest.json`
requested both `drive.file` and `documents`. Per Google's Docs API auth
docs, `drive.file` is itself an accepted Docs API scope covering
app-created files, which is all this app ever touches; `documents` grants
access to *every* Doc the user owns and is classified **sensitive**
(heavier verification, annual re-review) where `drive.file` is
**non-sensitive** (basic verification). Dropped `documents` from the
manifest and from `requiredScopes` in `auth.go`. Also dropped the
`host_permissions` entry for `googleapis.com` — the extension never calls
Google directly, the backend does. Done now because there are no users
yet, so nobody has to re-consent.

**Server hardening** (`cmd/server/main.go`):
- `http.Server` with read/header/write/idle timeouts and `MaxHeaderBytes`,
  replacing the bare `ListenAndServe`.
- Graceful shutdown on SIGTERM/SIGINT, 20s drain; a second signal exits now.
- `GET /healthz` (liveness, no deps) and `GET /readyz` (pings Postgres).
- CORS locked to `ALLOWED_ORIGINS` (comma-separated); unset still falls
  back to `*` for local dev but warns loudly at startup. Disallowed
  preflights get a 403. Tested in `cmd/server/main_test.go`.

**Request body limits** — all three handlers went through an unbounded
`json.NewDecoder(r.Body)`. Now a shared `decodeJSONBody` helper with
`http.MaxBytesReader`: 16 KiB for note/organize, 1 MiB for captures.
Over-limit returns 413.

**Docs API retries** (`internal/google/docs.go`) — up to 3 attempts with
backoff, honouring `Retry-After`. The subtlety worth preserving: reads and
`batchUpdate` retry on network errors and 5xx, but `CreateDoc` retries
**only on 429**, because a create has no idempotency key and replaying it
after an ambiguous 5xx could leave a stray empty Doc in the user's Drive.
`AppendCapture` now also sends `writeControl.requiredRevisionId` (from the
same documents.get that gave us the indices) — that is what makes its
retry safe, and it also makes a concurrent edit fail loudly instead of
inserting text at a stale offset. Covered by tests.

`go build`, `go vet`, `go test ./...` all clean. Still no end-to-end run:
no Postgres on this machine and Docker wasn't running, so the HTTP layer
has been exercised only through unit tests.

**Still open before launch** (checklist lives in the backend README):
request logging, per-user rate limiting, a migration tool, and the real
OAuth client id + `GOOGLE_OAUTH_CLIENT_ID` + `ALLOWED_ORIGINS` values.

## First working end-to-end run (2026-09-08)

Capture path is live against the real Google APIs: extension → Railway →
Postgres → Google Docs. Four things had to be fixed to get there, none of
them visible from the code alone:

1. **`ALLOWED_ORIGINS`** didn't match the extension id, so every request
   died at the CORS preflight with an undiagnosable `TypeError: Failed to
   fetch`. The extension id is derivable from the manifest `key`:
   base64-decode it, SHA-256, take the first 16 bytes, map each hex digit
   0-f onto a-p.
2. **`BACKEND_URL` was missing its `https://`.** `fetch()` treats a
   scheme-less URL as *relative to the extension's own origin*, so it
   silently requested `chrome-extension://<id>/flow-notes-...`. There is
   now a startup guard that logs an explicit error for this.
3. **The token had no identity scope.** The original auth plan said to use
   the tokeninfo `sub` as the user id — but `sub` is an OpenID Connect
   claim, absent from tokens carrying only `drive.file`. Fixed by adding
   `openid` to `manifest.json`'s `oauth2.scopes`. This was a flaw in the
   plan as written, not in its implementation.
4. **`drive.file` couldn't be selected in the console** because the scope
   picker only lists scopes for enabled APIs, and only the Docs API was
   enabled. The Drive API has to be enabled for the scope to appear, even
   though nothing ever calls a Drive endpoint.

The backend README carries the setup details. Also fixed along the way:
`popup.js` and `content.js` both discarded `response.error` and showed
generic strings ("Could not create note."), and `apiFetch` discarded the
response body — so every distinct failure looked identical. All three now
surface the real message, and `content.js` bounds the message round-trip
at 30s so a hung `getAuthToken` can't leave the UI stuck on
"Creating note…" forever.

**Debugging lesson worth keeping:** every one of these surfaced at the
extension as a generic error. The Railway log line was what actually
identified the scope problem. Check the server log before theorising.

## Local-file storage mode (2026-09-08)

Built and verified end to end. Decision taken with the user: local notes
still get a Postgres metadata row and still require Google sign-in, so the
note list stays unified. Local mode therefore means "my notes as files on
disk", **not** "works without a Google account" — the brief's other
motivation for local mode is still unaddressed, and would need the local
server to own its own index.

**New: `flow-notes-local/`** — its own Go module, stdlib only, so a user
can `go run .` with nothing to fetch.
- `POST /notes` creates `<dir>/<slug>.md` with a title heading,
  de-duplicating filenames; `POST /captures` appends the highlight plus a
  `[source](url)` link; `GET /healthz`.
- Binds `127.0.0.1` only. Default dir `~/FlowNotes` (`-dir`), port 4500
  (`-port`), allowed origin via `-origin`.
- **The security boundary is `Store.resolveExisting`**: a capture's path
  comes from the browser, so it is validated to be an existing regular
  `.md` file whose *resolved parent* equals the notes directory. Compares
  resolved paths, not string prefixes, so `../` and symlinks planted in
  the notes dir both fail. Verified by mutation — disabling the check
  makes `TestAppendCaptureRejectsPathsOutsideNotesDir` write to a file
  outside the directory.
- CORS: `-origin` pins one origin; unset allows any `chrome-extension://`
  with a startup warning (pages can't forge Origin, but other extensions
  aren't distinguished).

**Backend**: `POST /api/notes` accepts `local_path`, required for
`storage_target = "local"` and rejected otherwise. Requiring it means a
local note is never registered unless its file already exists.

**Extension**: storage-target picker in both the in-page popup and the
toolbar popup; `CREATE_NOTE` sends `storage_target`; `SAVE_CAPTURE`
carries the note's `storage_target` and `local_path` (content.js now keeps
the loaded notes in a Map, since the `<select>` can only hold an id).
Local file writes are best-effort with `local_sync: "ok" | "failed"`,
mirroring `drive_sync` — the capture is in Postgres regardless, so a
stopped local server never costs a highlight. `manifest.json` gained
`host_permissions` for `localhost:4500`.

**Ordering that matters**: the file is created *before* the metadata row.
The reverse would leave a note pointing at a file that was never created;
this way a failure leaves a stray Markdown file, which is harmless.

Verified by running the server: notes created and de-duplicated, captures
appended with working source links (including emoji and CJK), traversal
and cross-directory writes rejected, disallowed origins 403'd, preflight
from the real extension origin 204.

## Note deletion (2026-09-08)

Small delete button beside each note in the toolbar popup, removing the
note from Drive or the local filesystem along with its metadata row.

**Everything trashes; nothing is destroyed.** Drive notes go to the user's
Drive trash via the Drive API (`files.update` with `trashed: true` — the
Docs API has no delete); local notes move to a timestamped `.trash`
subdirectory of the notes directory. `internal/google/drive.go` is the
first and only Drive API call in the project.

**Ordering**: content is trashed *before* the metadata row is dropped. The
reverse would leave a file the app can no longer name. Both operations are
idempotent — a 404 from Drive and an already-missing local file both count
as success — so retrying a partial failure works, and a note can never
become impossible to remove from the list.

**UI**: two-click confirm on the × button (first click arms it and shows
where the content will go, second commits, auto-disarms after 4s). Not a
`confirm()` dialog, which can dismiss the extension popup out from under
itself. Deletion lives only in the toolbar popup, not the in-page capture
popup — the brief scopes the in-page surface to capture.

CORS on the backend now advertises DELETE; without it the browser blocks
the preflight.

Verified against a running local server: file moved to `.trash` with
contents intact, repeat delete idempotent, delete of a path outside the
notes directory rejected with the target file untouched.

## Apple Notes storage target (2026-09-08)

Third storage target, `apple_notes`, driven by the local server. Chosen by
the user, who already keeps notes in Notes.app and found the local-file
target too slow to open. Direction also set here: **persistent in-page
highlights are explicitly out of scope** — the product is about AI
organising, summarising and extending, not about being a better
highlighter.

**Mechanism**: `osascript` with AppleScript's `on run argv`, data passed as
process arguments. Nothing is interpolated into script source — capture
text comes from arbitrary pages and concatenation would make it
executable. All page text is additionally HTML-escaped, since Notes bodies
are HTML. Verified that osascript argv passing survives quotes,
ampersands, newlines and backslashes intact, and that all five scripts
pass `osacompile` (which also confirms Notes accepts the `note id X`
specifier and `exists folder X`).

**Files**: `applenotes.go` (interface + HTML rendering, all platforms),
`applenotes_darwin.go` (real implementation), `applenotes_other.go`
(`//go:build !darwin` stub returning 501). Still cross-compiles with
`GOOS=linux`.

**Endpoints**: `POST /apple/notes`, `/apple/captures`,
`/apple/notes/delete`, `/apple/notes/open`. The open route is the reason
this target exists — an extension cannot script Notes.app, so clicking a
note in the popup asks the local server to bring it up in Notes.

**Backend**: migration `internal/db/migrations/001_apple_notes.sql` widens
the `storage_target` CHECK and adds `apple_note_id`. **Run it before
deploying the code** — `ListNotes` selects `apple_note_id`, so against an
un-migrated database `GET /api/notes` 500s and the entire note list breaks,
not just Apple Notes creation. (This bit us: the code was deployed first.)

Also added: every 500 path in `notes.go` and `captures.go` now logs its
underlying error. Previously a failed query returned "failed to query
notes" to the client and wrote *nothing* to the server log, making it
undiagnosable from either end.

**Deletion** goes to Notes' Recently Deleted, consistent with Drive trash
and the local `.trash` directory. AppleScript error -1728 (no such object)
is swallowed so an already-deleted note can still leave the list.

**Not yet run against real Notes.app** at time of writing: syntax and
cross-platform builds verified, but the live path (which triggers the
macOS automation prompt and writes to the user's actual Notes) had not
been exercised.

## Chrome Private Network Access (2026-09-08)

The local server was running, healthy, and answering `curl` correctly,
while the extension got `TypeError: Failed to fetch` on every call to it.

Cause: Chrome's Private Network Access. A request from an extension
context to a loopback address is blocked unless the preflight response
carries `Access-Control-Allow-Private-Network: true`. The local server's
CORS middleware now echoes it when the browser sends
`Access-Control-Request-Private-Network: true` (and only then).

**Worth remembering**: "curl works but the browser says Failed to fetch"
against a localhost server is the signature of this, and the browser gives
no detail whatsoever. The service worker console names the PNA block
explicitly; nothing else does.

## Non-macOS behaviour for the Apple Notes target (2026-09-08)

Asked what a Windows user would experience. The server side was already
correct — `501` with "Apple Notes is only available on macOS", the local
Markdown target unaffected — but the extension still *offered* Apple Notes
in its storage picker, so a non-Mac user could pick a target that could
only ever fail.

Fixed: both popups now drop the option on non-Mac via a `GET_PLATFORM`
message to the worker (content scripts can't call
`chrome.runtime.getPlatformInfo` themselves). The 501 remains as a
backstop.

`flow-notes-local/routes_test.go` covers the unsupported-platform and
permission-denied paths with a fake store. That indirection is deliberate:
the real `!darwin` implementation cannot be compiled or executed on the
development Mac, so a fake at the interface boundary is the only way to
test what those users actually see.

## UI redesign — ruled paper (2026-09-08)

Replaced the dark navy/purple placeholder styling on both surfaces with a
warm ruled-paper theme, chosen by the user from references (Glasp,
notebook pages). Variant **A — Ruled** was picked over grid and plain
paper.

All CSS gradients, no image assets — the content script's styles land on
every page the user visits. Details that matter, all documented in
`flow-notes-extension/README.md`:

- Ruling pitch **is** the line-height (`--rule`), so text sits on the
  lines rather than drifting across them.
- Hover paints a highlighter swipe across the row, not a box — a box
  fights the ruling.
- Every colour is a token *including* the paper sheen. The first dark-mode
  pass hardcoded `rgba(255,255,255,0.55)` for it, which blew out the dark
  card and swallowed the header; caught by rendering the dark variant.
- Form controls need an opaque `--surface` and a `--edge` border distinct
  from `--rule-ink`. The first pass left them transparent, so the ruling
  ran through the selects and tangled with their borders — the user spotted
  it immediately in the in-page card.
- System serif for headings, system sans for body: extension pages cannot
  load remote fonts under the default MV3 CSP.

**Method worth reusing**: the styles were extracted from `popup.html` and
`content.js` into a standalone preview page and rendered in a browser,
showing light/dark and both pattern variants side by side. Far faster than
reloading the extension per tweak, and it is what surfaced the sheen bug.

Refined after review: rounded corners (needs
`html{background:transparent}` or Chrome paints white behind them); the
popup restructured into header / ruled writing area / foot so it reads as
a real page; ruling moved off `body` onto `#note-list` so rows align by
construction instead of depending on header height; `#empty-state` moved
inside the ruled area (so `loadNotes` clears `.note-item` children, not
`innerHTML`); and a "Creating note in …" progress state with the button
disabled in flight, matching what the in-page popup already did.

**Capture toggle** added to the masthead (`role="switch"` button, not a
styled checkbox, so it is keyboard-operable and announces state). It
pauses capture only — it deliberately does not disable the extension,
which would need the `management` permission and would be a one-way door.
`captureEnabled` in `storage.sync`; `content.js` listens on
`storage.onChanged` so open tabs react without a reload; `background.js`
shows an `off` badge so a paused extension is visible from the toolbar.
Defaults to on when the storage read fails.

Explicitly **not** built, at the user's direction: persistent in-page
highlight rendering. The product is about AI organising, not about being a
better highlighter.

## AI organize / summarise (2026-09-08)

The feature the project exists for. Two actions per note in the toolbar
popup: **Organise** (light) and **Summarise** (heavy), per the user's spec.

**Provider**: Groq free tier, OpenAI-compatible endpoint, so
`internal/ai/groq.go` is plain net/http — no new dependency. `GROQ_MODEL`
and `GROQ_BASE_URL` override the defaults, so any OpenAI-compatible
provider works. Verified against the current free-tier docs:
`llama-3.3-70b-versatile`, 30 RPM / ~12K TPM.

**Pipeline**: captures → `internal/ai` (prompt + call) → Markdown →
`internal/markdown` (blocks + UTF-16 style spans) → per target:
Drive via `google.ReplaceBody` (Docs batchUpdate), Apple Notes via
`Document.HTML()`, local files via the Markdown passed straight through.

**Design decisions worth keeping:**

- **Always regenerate from the capture log**, never edit the existing
  note. Postgres is the source of truth, so a bad pass destroys nothing
  and re-running is safe. Settles the brief's open question against
  incremental merge, which would need the model to identify its own prior
  output reliably.
- **`internal/markdown` is not general Markdown.** It parses exactly the
  subset the prompt permits. The prompt and the parser are ONE CONTRACT —
  change one without the other and constructs land in the user's note as
  literal punctuation.
- **Citations are enforced in the prompt**, not post-hoc: `[source](url)`
  only on material from that excerpt, never on model-written sentences.
  This is the brief's span-scoping rule.
- **Organize runs in the service worker, not the popup.** A popup closes
  on focus loss; for Apple Notes and local files the second step (writing
  the rendered content via the local server) would then never happen.
- **`mode: "no"`** rebuilds from raw captures with no model call — the undo.
- **Local file writes back up the previous version** into `.trash` first,
  since a hand-typed edit would otherwise vanish silently.

**Spacing (added after first real output looked cramped)**: blank lines in
the model's Markdown are structure — the parser eats them — so prompt rules
alone cannot produce visual spacing. Docs now uses `spaceAbove`/
`spaceBelow` paragraph styles (never empty paragraphs, which would be
cursor-addressable lines that survive export), with code blocks spaced only
at their outer edges since each line is its own Docs paragraph. Apple Notes
gets `<div><br></div>` between blocks but not between bullets in one list.
The prompt also gained layout rules, which matter for whether the output
parses into sensible blocks at all.

**Summarise was retuned after real output (2026-09-09)**: the first prompt
("expand on each idea, add examples, elaborate") produced far more than the
user wanted to read. Rewritten around restraint — the reader is explicitly
an intermediate returning to their *own* notes, the governing constraint is
a length budget (at most ~1.5x the excerpts), and the failure mode is named
outright ("adding to a passage that is already clear"). Specific padding
patterns are forbidden by name: restating excerpts, "why this matters",
summary/takeaways sections, padding thin sections to match fuller ones.
Where content is sufficient the instruction is to LEAVE IT and only gloss
hard terms in a clause; only genuinely thin sections get a sentence or two
of background.

Lesson: expansion instructions are an accelerator with no brake. A length
budget plus a named failure mode restrains output where "be concise" does
not.

**Then rewritten a third time (2026-09-09)**, because blanket restraint
over-shot in the other direction and suppressed the gap-filling that was
the actual goal. The brief the user gave: re-reading the note later should
require nothing to be looked up. Four levers, all asserted by
`TestSummarizeAndOrganizePromptsDiffer` so none can be quietly dropped:

1. **Purpose inference first** -- work out whether the reader is learning a
   new subject, collecting reference material, following an argument,
   gathering quotes or working a problem, *before* writing; and keep that
   conclusion out of the note. What helps a learner is not what helps
   someone collecting reference they already know.
2. **A concrete inclusion test** replacing the length target: would the
   reader, returning in a month, be stuck or have to look something up?
3. **Uneven expansion, deliberately** -- dense sections get almost
   nothing, cryptic ones get several sentences; equal lengths are padding.
4. **Accuracy over completeness** -- asking for jargon definitions invites
   confidently wrong ones, which beat missing ones only in appearance.

The arc across three versions is the lesson: "expand" produced bloat, a
hard length cap produced timidity, and only a purpose-driven test for what
to include produced the right shape.

**Gotchas hit and fixed:**
- The inline parser emitted one span per character (the fallback path
  consumes a rune at a time); runs are now coalesced in `emit`, or HTML
  would get a `<b>` per letter and Docs one styling request per letter.
- Docs request order matters: delete → insert → paragraph styles → text
  styles → bullets. Named styles carry text formatting so they must
  precede inline styles, and `createParagraphBullets` can rewrite
  paragraph content, so it goes last.
- The model call outlasts the server's 60s WriteTimeout;
  `http.NewResponseController` extends the deadline for that request only
  rather than loosening it globally.

**First real call failed** on `llama-3.3-70b-versatile`: "does not exist
or you do not have access to it", even though Groq's own docs list it as a
production model. **Model access is per-account, not just per-provider** —
the public docs are not the authority. `llama-3.1-8b-instant` is the
widely-available fallback, and `GROQ_MODEL` switches it with no redeploy.

In response, a model-unavailable error now calls `GET /models` and appends
the ids the key can actually reach, so the error answers its own question.
The match is on the specific "does not exist"/"do not have access" wording
rather than the word "model" — the first version fired on any 400
mentioning a model, costing a needless round-trip (caught by
`TestClientErrorIsNotRetried` counting two calls).

Otherwise still untested against a real model: the prompts have never met
a live LLM, and are the most likely thing to need tuning.

## Auto-start agent + distribution thinking (2026-09-09)

`flow-notes-local/install-agent.sh` installs the local server as a launchd
user agent (`com.flownotes.local`): builds to `~/.local/bin/`, runs at
login, restarts on failure, logs to `~/Library/Logs/`. `--uninstall`
reverses it.

**The fixed install path is load-bearing**: macOS ties the Notes automation
permission to the binary's location, so a moving path means re-granting it
every time — which is also why `go run` cannot be used for the agent, since
it builds to a fresh temp path on each run. Expect one more permission
prompt after installing, because the request now comes from
`flow-notes-local` rather than from Terminal.

**Distribution options discussed** (nothing chosen yet):
1. Don't ship the local server — Drive-only for other users, zero install.
2. Host binaries on GitHub Releases and link from the extension, prompting
   only when a local storage target is chosen. Recommended when someone
   other than the author wants Apple Notes. Catches: Gatekeeper warns on
   unsigned downloads (Apple Developer Program, $99/yr, to avoid), and the
   user must restart it after reboot without an agent like the above.
3. **Native messaging** — Chrome launches the helper over stdio on demand.
   Would eliminate the entire class of problems this project hit (Private
   Network Access, CORS, port conflicts, "is it running?"). Feasible
   because `Store` and `AppleNotesStore` are already separate from the HTTP
   layer: it is a transport swap, not a rewrite. Still needs an installer
   to place the host manifest. **Decide before distributing** — switching
   later breaks existing installs.
4. Bundling the binary inside the extension — rejected. Chrome Web Store
   has rejected packages containing executables, and shipping Go source
   instead would require users to have a Go toolchain.

Known gap either way: the local server's `/healthz` reports no version, so
extension/server drift would fail confusingly.

## Distribution: unsigned .zip + setup flow (2026-09-09)

User chose the unsigned-zip path over paying for Apple signing.

**`flow-notes-local/make-release.sh`** builds darwin arm64+amd64, bundles
them with `install-agent.sh`, README and a plain-language `SETUP.txt`, and
zips to `dist/`. `install-agent.sh` now prefers a bundled binary and falls
back to building from source, so one script serves both a downloaded
release (no Go needed) and a source checkout. It clears the quarantine
xattr, since unsigned downloads are otherwise refused by Gatekeeper.
Verified: the zip builds and the packaged binary runs and reports its
version.

**Version handshake**: local server `/healthz` now reports `version`
(const in `main.go`); the extension holds `HELPER_MIN_VERSION` and shows
an update prompt when the helper is older. **Bump both together** when the
endpoints change. `compareVersions` is numeric per part — string
comparison would rank "0.9.0" above "0.10.0".

**Setup panel** in the toolbar popup, shown **only when local storage is
in play** (a local-target note exists, or the picker is set to one). Drive
users never see it — which also keeps the Web Store listing honest about
what is required. Three states: missing, outdated, hidden. The install
command is rendered with `chrome.runtime.id` already filled in and copies
on click.

`HELPER_DOWNLOAD_URL` in `background.js` is a placeholder until the first
GitHub release exists.

**Keep the shell scripts ASCII-only.** The first run of `install-agent.sh`
died with `PLIST?: unbound variable`: under a UTF-8 locale `/bin/sh` pulls
a multi-byte character following a variable reference into the variable
NAME, so `"$PLIST<ellipsis>"` looked up an unset variable and `set -u`
aborted. `sh -n` passes it — the fault happens at expansion, not parse —
so it survived the syntax check. Both scripts are now ASCII with a comment
saying why. The installer also refuses to run when something already holds
the port, since a hand-started copy would otherwise block the agent
silently.

Two further installer bugs, both found by the user running it: (1) it ran
`launchctl kickstart -k` right after `bootstrap`, killing and restarting
the job that `RunAtLoad` had just started; (2) it then checked health once
after a fixed `sleep 1`, landing in the middle of that restart and
reporting failure for a server that was running perfectly. Now bootstrap
only, and the check polls for up to 5s. **Lesson: do not guess process
launch time with a fixed sleep** -- a false failure report is worse than
waiting.

Still open: native messaging (option 3) remains the better architecture if
this ever goes to non-technical users, and switching after people have
installed breaks them. Decide before publishing widely.

## Native messaging migration (2026-09-09)

Switched the extension-to-helper transport from localhost HTTP to Chrome
native messaging, deliberately **before** distributing to anyone — after
people install, switching breaks them.

**Structure**: `api.go` holds the transport-independent `API` (methods,
error codes); `nativemsg.go` is the stdio transport Chrome uses;
`http.go` keeps the HTTP server behind `-serve` for development, because
being able to curl the helper is how most of this project's integration
bugs were found. Both transports dispatch into the same `API`, so they
cannot drift.

**Wire format**: 4-byte native-endian length + JSON. Failures come back in
band (`ok:false`, `code`, `error`) so one bad call cannot kill the channel
— covered by a test asserting a later call still succeeds. `code` is
`invalid` / `unsupported` / `forbidden` / `failed`, so the extension never
infers a cause from prose.

**Everything diagnostic goes to stderr**: stdout is the wire, and a stray
byte there corrupts framing and hangs the channel silently.

**What this deleted**: CORS, the Private Network Access header, the port
and origin flags in production, and the whole launchd agent. `install.sh`
replaces `install-agent.sh`, writes the host manifest to every
Chromium-family browser present, and removes the old launchd agent if it
finds one.

**Verified end to end** by driving the built binary exactly as Chrome does
— framed messages on stdin, argv[1] set to the extension origin — not only
through Go tests.

**Costs, for the record**: free in money (native messaging is a browser
feature; the $99/yr Apple fee is code signing, a separate and unchanged
problem). Chrome's limits are 64 MiB extension-to-host and 1 MB
host-to-extension; note content travels in the generous direction, and an
over-sized reply is replaced with an error rather than dropped silently.

`Version` is now 0.4.0 and `HELPER_MIN_VERSION` matches — the method set
changed, so both had to move together.

## First-run fix: inject into open tabs (2026-09-09)

Chrome only auto-injects content scripts into pages loaded after an
install or update, so a new user's very first highlight — on the page they
were already reading — did nothing, with no hint that a reload was needed.
It hit every user at their first interaction.

`injectIntoOpenTabs` in the worker runs on `onInstalled` and injects into
every open http(s) tab. `content.js` now removes any pre-existing popup
host at startup, because after an update the old instance is still in the
page but orphaned; without that, two instances fight over the same
selection. This also removes the "reload the page after reloading the
extension" annoyance during development.

Cost: the `scripting` permission and `http(s)://*/*` host permissions. Not
a real escalation — the content script already matches all URLs and
produces the same install warning — but broad host permissions attract
Web Store reviewer attention, so justify it in the listing.

**Confirmed no secrets ship in the extension package.** The OAuth client
id and extension key are public by design; the Groq key, database URL and
everything else live only on the backend.

## Privacy: the server stores no captured content (2026-09-09)

User's call, taking privacy over the safety net: the `captures` table is
gone (`002_drop_captures.sql`). Postgres is now a pure index -- note id,
Google `sub`, title, storage target, and where the content lives. No
captured text, no source URLs, no bodies. Also rejected, and why: the
user suggested asking each user for their own Postgres URL. That URL
carries a password, the extension cannot open TCP so it would have to send
it to the backend, and the backend would then hold every user's database
credential -- strictly worse than holding their highlights.

**What it cost, both accepted:**
1. **A capture can fail and be lost.** `POST /captures` now fails the
   request instead of reporting partial success, so the popup says "Not
   saved: ..." and leaves the selection live. `drive_sync`/`local_sync`
   are gone -- there is no half-saved state any more.
2. **Organize compounds** rather than regenerating from an immutable log.
   Undo is each target's own version history: Drive revisions, Notes'
   Recently Deleted, and the local `.trash` (which `note.write` fills
   before overwriting). `ModeNone` was removed -- it existed to rebuild
   from the capture log, which no longer exists.

**Reading content back** is the new machinery: `google.ExportMarkdown`
for Drive; `note.read` / `apple.read` on the helper for the other targets,
with the extension sending the content up in the organize request.

**Two traps found, both about citations being the only remaining record of
provenance:**
- Docs stores the marker as the literal text `[source]` plus a link style,
  so wrapping it on export produced `[[source]](url)` -- unparseable, and
  it would have silently dropped the citation on the next pass. Export now
  reuses the existing brackets.
- `apple.read` must return `body` (HTML), not `plaintext`: plaintext drops
  every href, so a note organized twice would lose all its citations.
- Docs underlines links by default, so re-emitting underline would put
  `<u>` on every citation. Export skips underline when a link is present.

Verified the whole cycle against the real helper binary over native
messaging: create, append twice, read (citations intact), write back
(previous version backed up to `.trash`), read again.

Versions moved to 0.5.0 on both sides -- the method set changed.

Still open: organize has no in-app undo. A "restore previous version"
action would beat telling users to dig through Drive revisions or
`.trash`.

## Extension consistency checks (2026-09-09)

The "Download helper" button did nothing, because `HELPER_DOWNLOAD_URL` is
still the `YOUR-USER` placeholder. Fixed properly: the worker reports
`downloadConfigured`, and while it is false the panel hides the button and
tells the user to run `./install.sh` from the source folder. A placeholder
that silently does nothing is worse than one that admits it is not set.

Investigating it exposed something worse: an earlier slice-based edit of
the `SAVE_CAPTURE` case had **deleted `DELETE_NOTE`,
`LOCAL_SERVER_STATUS` and `GET_PLATFORM`** along with it, and nothing
caught it -- `node --check` only validates syntax, and the extension has
no tests. Restored, and `flow-notes-extension/check.mjs` now guards
against a repeat:

- every message type the popups send has a case in the worker
- every `callHelper` method exists in the helper's `api.go`
- every element id `popup.js` touches exists in `popup.html`
- permissions the code uses are declared in the manifest

**Run `node check.mjs` alongside `node --check` on every extension
change.** Two lessons: prefer anchored replacements over index-slicing
when editing code, and a syntax check is not a verification.

## Opening local notes (2026-09-09)

Clicking a local-file note copied its path, leaving the user to find the
file and pick an editor. It now opens: `note.open` on the helper hands the
file to the OS (`open` / `xdg-open` / `start`), with `-editor` (or
`FLOW_NOTES_EDITOR`) to name a specific application and `reveal: true` to
show it in Finder instead. Copying the path is now only the fallback when
the helper is unreachable.

**The containment check is what makes this safe.** `OpenNote` routes
through `resolveExisting` like every other browser-supplied path —
handing an unvalidated path to the OS open command would turn a crafted
request into arbitrary launch. Arguments go as a list, never through a
shell. Verified over the wire: a path outside the notes directory is
refused, and a non-existent editor fails cleanly rather than hanging.

Versions moved to 0.6.0 on both sides.

## Pre-launch backend work (2026-09-09)

Four things built together, all previously flagged and unbuilt.

**Per-user model keys.** `POST /organize` accepts `X-Groq-Key`; the header
wins over the server key for that request. The server key becomes an
optional fallback, so a public deployment can require every user to bring
their own and stop sharing one free-tier quota. `Client.WithKey` returns a
**copy** -- mutating the shared client would leak one user's key into
another user's call, which a test pins down. Keys containing whitespace or
control characters are rejected before they can corrupt an Authorization
header. Extension side: `chrome.storage.sync`, a password field cleared
after saving, and the popup only ever learns a boolean plus a
first-four/last-four hint, never the value.

**`DELETE /api/user/data`.** Erases the index and deliberately leaves the
user's Docs, Apple notes and files alone -- they are the user's property
and hold the only copy of their writing, so destroying them in response to
"delete my data" would be the wrong reading. The response says so.

**Rate limiting.** Per-user fixed windows inside `RequireAuth` (10/min for
organize, 120/min otherwise), `429` with `Retry-After`. Metered by account,
not address -- an address punishes everyone behind one NAT. In-memory, so
per instance; noted as a scaling limit.

**Request logging.** One line per request: method, path, status, duration,
size. **No headers, no bodies, ever** -- `Authorization` holds a Google
token and `X-Groq-Key` holds a user's key, and the way to keep credentials
out of logs is to have no code path that could write one. The wrapper
implements `Unwrap()` so `http.ResponseController` still reaches the real
writer -- organize needs that to extend its write deadline, and a test
guards it.

Also fixed: `/healthz` reported `version: "unknown"` in production because
Railway builds without git metadata, which made "is my deploy live?"
unanswerable -- the exact question that field exists to answer. It now
falls back to `RAILWAY_GIT_COMMIT_SHA` and friends.

## The original plan for the above (kept for reference)

I was mid-way through building Google Drive/Docs API integration in the
Go backend, in this chat (before switching to Claude Code). Nothing has
been written for this yet except the plan. Here's the intended design,
already reasoned through — implement it as-is unless you spot a real issue:

**New file: `internal/google/docs.go`**
- `CreateDoc(ctx, accessToken, title string) (documentID string, error)`
  — POST to `https://docs.googleapis.com/v1/documents` with `{"title": title}`.
- `AppendCapture(ctx, accessToken, documentID, text, sourceURL string) error`
  — appends a raw captured highlight to the end of the doc, followed by a
  small `[source]` marker that is itself a hyperlink back to `sourceURL`
  (NOT the quoted text itself — this was a deliberate decision, see the
  project brief's "source linking" section for why).
  - Steps: GET the document to find the end index (`body.content[last].endIndex - 1`),
    then POST a `batchUpdate` with two requests: an `insertText` at that
    index, and an `updateTextStyle` with `textStyle.link.url` scoped to
    just the `[source]` marker's index range.
  - **Important**: the Docs API indexes text in **UTF-16 code units**, not
    bytes or Go runes — use `unicode/utf16` (stdlib, no new dependency)
    to compute correct offsets when locating the marker span. This is a
    common source of subtle bugs if skipped.
- No new external Go dependencies needed — plain `net/http` + `encoding/json`
  + `unicode/utf16`, consistent with the rest of the backend's minimal-deps
  approach so far (only `pgx` is an external dependency currently).

**Wire it in:**
- `internal/handlers/notes.go` → `CreateNote`: when `storage_target == "drive"`,
  call `google.CreateDoc` and store the returned id as `drive_file_id` before
  inserting the DB row.
- `internal/handlers/captures.go` → `AddCapture`: after saving the capture to
  Postgres (which stays the source of truth regardless of outcome below),
  if the note's `storage_target == "drive"`, also call `google.AppendCapture`
  to push it into the Doc. Don't fail the whole request if this Drive call
  errors — the DB write already succeeded; consider surfacing a
  `"drive_sync": "failed"` flag in the response instead.
- Both of these need the raw access token, not just the user id — check
  `internal/handlers/auth.go`, which currently only extracts a `userID`
  from the bearer token as a dev-mode stub. **This also needs upgrading**:
  replace the stub with real verification against
  `https://oauth2.googleapis.com/tokeninfo?access_token=...`, extract the
  `sub` claim as the verified user id, and store the raw token in the
  request context too (a second context key) since Drive/Docs calls need
  it — the same token the extension already gets via
  `chrome.identity.getAuthToken()` already carries the `drive.file` and
  `documents` scopes declared in `manifest.json`, so no separate OAuth
  flow is needed on the backend side.

## After that, remaining roadmap (from the project brief)

1. Local server (separate small process) for local-file storage mode.
2. `POST /api/notes/{id}/organize` — currently a stub returning 501;
   needs the Groq (Llama) API call, mode-specific prompting (no/low/high),
   and write-back logic that forks by storage target (Docs API vs. local
   file writer).
3. Extension UI: "Organize" trigger (button not built yet), storage-target
   picker on note creation (currently `CREATE_NOTE` message only sends a
   title, defaults to "drive" server-side).

## Practical notes for Claude Code

- ~~The backend has never been compiled~~ — done: `go mod tidy`,
  `go build ./...`, `go vet ./...`, `go test ./...` all pass as of the
  session above, and `go.sum` is committed.
- Needs a real Postgres instance (local or free-tier hosted) and a Google
  Cloud OAuth client ID (`manifest.json`'s `oauth2.client_id` is still a
  placeholder) before end-to-end testing is possible.
