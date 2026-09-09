# Flow Notes — Extension (v0.2)

## What's here

- `manifest.json` — MV3 manifest. `oauth2.client_id` is a placeholder —
  you'll need a real Google Cloud OAuth client ID once Drive integration
  is wired up (Google Cloud Console → APIs & Services → Credentials).
- `content.js` — selection detection (Selection API) + Shadow DOM popup.
  Shows a note picker + save button on any page when you highlight text.
- `background.js` — service worker. Owns auth token retrieval and all
  `fetch()` calls (to the backend and, later, to the local server).
  `BACKEND_URL` currently points at `http://localhost:8080` — update
  once the Go backend is running or deployed.
- `popup.html` / `popup.js` — toolbar icon UI. Lists notes, opens Drive
  notes in a new tab. Local-file notes currently just show their path
  as a placeholder (opening a local file from the extension isn't
  possible — no filesystem access from browser JS).
- `icons/` — placeholder icons (solid color circles). Swap these out
  whenever you're ready for real branding.

## What this skeleton does NOT do yet

- No backend exists yet (`GET_NOTES` / `CREATE_NOTE` / `SAVE_CAPTURE`
  messages will fail until the Go backend's API routes exist).
- No Drive/Docs API integration yet — that lives in the backend, not
  the extension.
- No local-server capture path yet — `SAVE_CAPTURE` always routes to
  the backend regardless of a note's storage_target; branching logic
  for local-mode notes needs to be added to `background.js` once the
  local server exists and note metadata includes `storage_target`.
- No organize UI yet — `ORGANIZE_NOTE` message handler exists in
  `background.js` but nothing in the UI triggers it yet.

## Settings

Folded away behind a "Settings" link at the foot — the popup's job is the
note list, and a key set once should not occupy it permanently.

**Your own Groq key.** Stored in `chrome.storage.sync` (so it follows the
Chrome profile) and sent as `X-Groq-Key` only on organize requests. The
popup asks the worker whether a key is set and gets back a boolean plus a
first-four/last-four hint — never the value. The input is `type="password"`
and is cleared after saving, so a credential is never left sitting in a
visible field.

**Delete my data.** Two-click confirm, like note deletion, since it is not
recoverable. It removes the server's index only; the wording is explicit
that the user's own Docs, Apple notes and files are untouched.

## Packaging for the Chrome Web Store

```
cd flow-notes-extension && ./package.sh
```

Ships **only** `manifest.json`, the three scripts, `popup.html` and
`icons/`. `README.md` and `check.mjs` are development files: including them
puts unused code in the package, which reviewers flag, and `check.mjs`
would read as a build script that never runs. The script refuses to build
if the checks fail, and refuses to emit a package containing a README,
`.env`, `.DS_Store` or a stray archive.

Zip the **folder contents**, never the repository root -- a package
containing sibling projects will be rejected.

## Checks

```
node check.mjs
```

`node --check` only validates syntax, so it happily accepts a popup
sending a message type the worker has no case for. That is not
hypothetical: three handlers (`DELETE_NOTE`, `LOCAL_SERVER_STATUS`,
`GET_PLATFORM`) were once deleted by a careless edit and nothing caught
it. `check.mjs` verifies that

- every message type the popups send has a case in the worker
- every `callHelper` method exists in the helper's `api.go`
- every element id `popup.js` touches exists in `popup.html`
- permissions the code relies on are declared in the manifest

Run it alongside `node --check` on every change.

## UI: the ruled-paper theme

Both surfaces — the toolbar popup (`popup.html`) and the in-page selection
popup (the shadow DOM in `content.js`) — share one small design system.
It is entirely CSS gradients: **no image assets**, which matters because
the content script's styles are injected into every page the user visits.

Three rules keep it coherent; break them and it stops reading as paper:

1. **The ruling pitch and the line-height are the same number** (`--rule`,
   26px in the popup and 24px in the card). Text sits *on* the lines
   instead of drifting across them. Change one, change the other.
2. **One accent, one danger colour.** The accent is a highlighter wash
   (`--highlight`) that paints across a row on hover — a swipe, not a box,
   because boxes fight the ruling. Everything else is ink on paper.
3. **Every colour is a token**, including the paper's top sheen
   (`--sheen`). Hardcoding white there blows out the dark theme and
   swallows the header — this actually happened during the redesign.
4. **Form controls sit ON the paper, never let it show through.** Selects
   and secondary buttons use `--surface` (opaque, slightly lighter than
   the paper) with a `--edge` border. Transparent controls let the ruling
   run straight through them and collide with their borders, which looks
   like a rendering bug rather than a design. `--edge` is deliberately not
   `--rule-ink`: a border in the ruling's own colour reads as more ruling
   rather than as the edge of a control.

The red margin rule in the toolbar popup is `.sheet::before`, and it is
why the content is indented rather than centred.

### Page structure

The toolbar popup is laid out as an actual page, in three bands:

- **Header** — the eyebrow and title, closed by a `3px double` rule in the
  margin line's red. The double rule is what makes it read as a masthead
  rather than as text that happens to be at the top.
- **Writing area** (`#note-list`) — this is the only element that carries
  the ruling. It used to live on `body`, which meant the lines had to line
  up with a header of arbitrary height; they can't be relied on to. Owning
  the ruling here makes alignment automatic, because a row's line-height
  is the ruling pitch. Its `min-height` keeps a few empty lines below the
  last note so the popup reads as a page with room left to write.
  `#empty-state` lives *inside* it so the empty message sits on a line —
  which is why `loadNotes` removes `.note-item` children rather than
  clearing `innerHTML`.
- **Foot** — the storage picker and New note button, on clean paper.

### Organise / Summarise

Each note row reveals **Organise**, **Summarise** and delete on hover, in
the space the storage chip occupies — the chip is reference information,
the actions are what you came to do, so the row trades one for the other
rather than getting wider.

Both actions run in the **service worker**, not the popup. For Apple Notes
and local files the backend can only render the new content; something on
this machine must write it. If that second step lived in the popup,
clicking away mid-run would leave the note un-updated with no indication
why. The popup shows progress if it is still open, and the work completes
either way.

The whole list locks while a pass runs (`.note-list-busy`), since a second
pass would race to rewrite the same note.

### Local helper setup

Apple Notes and local files need a helper program on the user's machine;
Drive-backed notes don't. So the setup slip appears **only when local
storage is actually in play** — a local-target note exists, or the picker
is set to one. Someone using Drive is never nagged to install anything,
which also keeps the Web Store listing honest about what's required.

`LOCAL_SERVER_STATUS` (in the worker) answers with a *result* rather than
throwing: "not installed" is a normal state, not an error. It reports
reachability, the helper's version, and whether that version is older than
`HELPER_MIN_VERSION`.

The helper is reached by **native messaging** (`callHelper` in
`background.js`), not HTTP. Chrome launches it on demand and enforces
access through the host manifest's `allowed_origins`, which is why the
extension needs no `host_permissions` for localhost and the helper needs
no CORS, no Private Network Access header and no port. `sendNativeMessage`
rejects when the host manifest is missing or doesn't list this extension —
that rejection is what the setup panel reacts to.

Two details worth keeping:

- The install command is rendered with `chrome.runtime.id` filled in, and
  clicking it copies it. Nobody should have to go and find their extension
  id on `chrome://extensions`.
- `compareVersions` compares numeric parts, not strings — `"0.10.0"` is
  newer than `"0.9.0"`, and string comparison gets that backwards.

`HELPER_DOWNLOAD_URL` in `background.js` is a **placeholder** until the
first GitHub release exists. While it is unset, the setup panel hides the
Download button and tells the user to run `./install.sh` from the source
folder instead — a placeholder link that silently does nothing when
clicked is worse than one that admits it is not configured.

### Injecting into already-open tabs

Chrome only auto-injects content scripts into pages loaded *after* an
install or update. Without intervention, a new user installs, highlights
something on the page they were already reading, and nothing happens —
with nothing to tell them a reload is needed. That lands on every user at
their very first interaction.

`injectIntoOpenTabs` (worker, on `onInstalled`) injects into every open
http(s) tab. Chrome refuses its own pages, the Web Store and the PDF
viewer; those failures are expected and logged at debug level only.

`content.js` removes any pre-existing popup host on startup, because after
an *update* the previous instance is still in the page but orphaned — two
instances would otherwise fight over the same selection.

This is what `scripting` and the `http(s)://*/*` host permissions are for.
They do not widen what the extension can reach: the content script already
matches all URLs, which produces the same install warning. Worth stating
plainly in the Web Store listing, since broad host permissions attract
reviewer attention.

### Capture toggle

The switch in the masthead pauses capture: the in-page selection popup
stops appearing, while the notes list stays fully usable.

It does **not** disable the extension itself. That would need the
`management` permission and would be a one-way door — a disabled extension
can't re-enable itself, so the user would have to go to
`chrome://extensions` to undo it.

State lives in `chrome.storage.sync` under `captureEnabled`, so it follows
the Chrome profile. Three pieces stay in step:

- `popup.js` renders and writes it (defaulting to **on** if the read
  fails — a broken read must not silently stop capture)
- `content.js` reads it once and then listens on `storage.onChanged`, so
  already-open tabs react immediately instead of needing a reload
- `background.js` puts an `off` badge on the toolbar icon while paused,
  because otherwise "why isn't the highlight popup showing?" has no answer
  from the page itself

`html { background: transparent }` is what lets the body's `border-radius`
show; without it Chrome paints a white rectangle behind the popup.

Both surfaces adapt to `prefers-color-scheme: dark` by redefining tokens
only. Headings use a system serif (`ui-serif`/Georgia) and body text a
system sans — extension pages can't load remote fonts under the default
MV3 CSP, and shouldn't want to.

### Previewing changes without reloading the extension

The styles can be extracted from `popup.html` and `content.js` into a
standalone page and opened in a browser, which is far faster than
reloading the extension for every tweak. That is how the current theme was
built and reviewed, including its dark variant.

## How to load this for testing right now

1. Go to `chrome://extensions`
2. Enable Developer mode (top right)
3. Click "Load unpacked", select this folder
4. Highlight text on any page — the popup should appear (it'll show
   "Could not load notes" until the backend exists, which is expected)

## Suggested next steps

1. Scaffold the Go backend: DB schema (`notes`, `captures` tables),
   `/api/notes` GET/POST, `/api/notes/:id/captures` POST.
2. Point `BACKEND_URL` in `background.js` at it, confirm the popup's
   note list populates and captures save.
3. Add Drive OAuth + Docs API integration to the backend.
4. Build the local server (separate small Go or Node process) and
   wire up the local-mode branch in `background.js`.
5. Add the `/api/notes/:id/organize` endpoint (Groq/Llama call) and
   an "Organize" button somewhere in `popup.js`.
