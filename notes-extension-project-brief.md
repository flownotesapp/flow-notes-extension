# Project Brief: Frictionless Reading-to-Notes Chrome Extension

## Purpose / Problem Statement

I read articles, research papers, and blogs to learn. When I find a useful definition or line, my current workflow is: select the text → copy it → switch to my notes app → paste it → later, manually organize it into the right topic/file.

This breaks my reading flow in two ways:
1. **Context switching** — leaving the page to paste disrupts the act of reading.
2. **Loss of context on re-reading** — a pasted line, stripped of its surrounding structure, often reads awkwardly or confusingly later, since it was written for a different document's flow.

## Goal

Build a Chrome extension that lets me capture highlighted text directly into my notes **without leaving the page I'm reading**, and later turn those raw captures into a well-organized note — without forcing AI processing into the moment of capture (which would slow down reading).

## High-Level Flow

1. I'm reading a page. I select/highlight a sentence or paragraph.
2. A small popup appears immediately near the selection, in-page (like the native "copy/define" selection popups browsers show).
3. The popup lets me pick which note to save it to (or create a new note), and saves the raw text immediately — no AI involved at this step.
4. Later — whenever I choose to — I can trigger an "organize" pass on a note, where AI restructures/elaborates the raw captured content into something more readable.
5. All notes are listed inside the extension. Since notes are stored as Google Docs in my own Drive, clicking a note in the extension's list opens/redirects me to that Doc in Drive — the extension itself is not where I read or edit note content.

## Architecture Overview

The system has four distinct, separably-buildable pieces:

1. **Content script (capture)** — runs on every page, detects text selection via the browser's Selection API, renders an in-page popup (via Shadow DOM to avoid CSS collisions with the host page), and sends the captured text + metadata to the backend.
2. **Extension UI (index)** — toolbar popup and/or side panel showing a list of existing notes (title + last updated), "create new note," and "organize this note" actions. Does NOT render note content itself.
3. **Backend service** — receives captures, manages note metadata in a database, talks to Google Drive/Docs APIs to read/write actual note content, and orchestrates AI organize calls.
4. **Storage** — actual note content lives as Google Docs in the user's own Google Drive (via OAuth, `drive.file` scope — access limited to files the app itself created, not the whole Drive). The backend database stores only lightweight metadata (note id, title, Drive file id, last updated, tags), acting as an index/cache, not the source of truth for content.

## Key Design Decisions Already Made (and why)

- **Database backend chosen for scalability** — over local-only (`chrome.storage`) or piggybacking entirely on a third-party notes app's API.
- **Google Drive as the actual content store, not just the DB** — so the user's notes remain accessible/owned independent of whether my service keeps running; the DB is metadata-only, Drive holds the real content. Uses `drive.file` OAuth scope specifically (least-privilege — the app can only see files it created, not the user's whole Drive), which is both more trustworthy to users and easier to pass Google's and Chrome Web Store's review.
- **Notes are stored as Google Docs** (not plain `.md`/`.txt` files in Drive) — because clicking through from the extension should land on something pleasant to read/edit natively, and Docs' native editor gives that for free. Trade-off: the backend must talk to both the Drive API (listing/metadata) and the Docs API (reading/writing actual content and applying formatting like hyperlinks).
- **AI processing is decoupled from capture ("capture now, organize later")** — highlighting a selection only ever does a fast, cheap append of raw text; no LLM call happens at that moment. This was chosen deliberately over an earlier "AI mode at insertion time" design, because doing AI work synchronously during capture would force compromises (limited context window, added latency) exactly at the moment flow matters most (mid-reading). Decoupling removes that pressure entirely — "organize" is a deliberate, later action where slower/richer AI context is an acceptable trade since it's off the reading critical path.
- **Two representations of note content**:
  - A **raw, append-only capture log** (text, source URL, source page title, timestamp) — the immutable source of truth.
  - An **AI-organized version**, regenerable at any time from the raw log — so a bad AI pass never destroys original captures, and re-organizing with a different mode/prompt later is always safe.
- **AI "organize" has three modes, chosen by the user when they trigger it (not at capture time)**:
  - **No** — no AI involvement; content stays as raw captured text.
  - **Low** — light-touch: places captured text into a sensible spot in the note and lightly smooths phrasing so re-reading feels natural, without adding new information.
  - **High** — heavier: elaborates, adds explanatory content, restructures more substantially.
- **Every non-AI-generated (raw captured) span of text in a note must link back to its original source page.** Preferred implementation: an inline citation marker (e.g., a small "[source]" tag) that is itself a hyperlink, rather than hyperlinking the quoted text directly — because an explicit visible marker signals "this line came from elsewhere" at a glance, which matters for the original goal of not losing context on re-reading. Implemented via the Docs API's `updateTextStyle` with a `link` field, scoped to just the marker span.
- **Sub-decision this creates, to be carried into implementation**: when "high" mode elaborates on a raw span, the source link must stay attached to the original quoted portion only, not to AI-added surrounding content. This means captured spans need to be tracked as identifiable units (not just loose text) through the organize pipeline, so the AI step can be instructed to preserve/re-wrap the original span's link and NOT extend that same link to newly generated content.
- **Extension is the only surface I interact with for finding/managing notes, but not for reading/editing content** — the extension shows the notes list and handles capture; opening a note always redirects to the Google Doc in Drive for actual reading/editing. I explicitly do not want a full in-extension note viewer/editor.

## Must-Have Functionality (v1)

- Selection popup on any webpage, triggered by highlighting text, positioned near the selection (using `Selection.getRangeAt().getBoundingClientRect()`), rendered via Shadow DOM for style isolation.
- Popup shows: list of existing notes to save into, an option to create a new note, and a save action — no AI choice at this step.
- Each capture stores: raw text, source URL, source page title, timestamp.
- Extension-side "all notes" list view (title + last updated), backed by DB metadata, not live Drive queries.
- Clicking a note in the extension opens the corresponding Google Doc in a new tab.
- Google OAuth via `chrome.identity.getAuthToken()`, `drive.file` scope only.
- Backend can create/update Google Docs via the Docs API, including applying hyperlink formatting to specific text spans.
- An "Organize this note" action (triggered manually by the user, not automatic) that runs one of the three AI modes (no/low/high) against the raw capture log and writes/updates the organized content in the Doc, preserving source links appropriately.

## Should-Have / Later Functionality

- Periodic (rather than purely on-demand) AI organization, once on-demand is proven out.
- Smarter default note suggestion in the popup (e.g., "add to most recently used note" as a one-click default, with "choose a different note" as a secondary action) to reduce the small flow-break of picking from a list every time.
- Fallback to local-only storage (`chrome.storage`) for users not signed into a Google account or who decline Drive permission, if Drive access should not be mandatory.
- Export/sync to other note tools (Obsidian, Notion) as alternate destinations, if Drive-only proves limiting later.

## Explicitly Out of Scope / Rejected Approaches

- No AI call happens synchronously at the moment of text capture — rejected in favor of decoupled, on-demand organization.
- No in-extension note reading/editing UI — notes are always read/edited in Google Docs via Drive; the extension is capture + index only.
- No broad Drive access (`drive` full scope) — only `drive.file`, scoped to app-created files.
- No plain-text/Markdown file storage in Drive — Google Docs chosen instead, for a better native reading/editing experience on click-through.

## Still Undecided (to work through during design)

- Whether Drive access/sign-in is mandatory to use the extension at all, or whether a local-only mode should exist as a fallback/lower tier.
- Whether re-running "organize" on a note regenerates the organized version from scratch each time, or attempts to incrementally merge new raw captures into the existing organized content.
- Exact granularity of how captured spans are tracked as identifiable units through the AI organize pipeline (needed to correctly preserve/scope source links through "high" mode elaboration).
- Whether "high" mode's added context should draw only from the destination note's own content, from the original source page's surrounding text, from general knowledge, or some combination — this affects what context gets sent to the LLM and at what cost/latency.

## About Me / Constraints Relevant to Design Choices

- I'm comfortable with technical trade-off discussions (backend design, API choices, data modeling) — no need to oversimplify.
- My current personal note-taking habit is a flat list without headers, though I'd genuinely prefer sectioned/structured notes if it doesn't add friction to maintain myself.
- My priority is keeping the *capture* step as low-friction as possible, even if that means the *organizing* step is a separate, more deliberate action I trigger later.
- I want to avoid over-engineering this into a full notes-app rebuild — the extension's job is capture + indexing + triggering organization, not being the primary reading/editing surface.

---

**What I'd like from you:** Help me move from this design into an actual implementation plan — starting with the manifest, content script, and backend API shape, and working through the undecided items above as they become relevant.
