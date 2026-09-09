// content.js
// Detects text selection on any page, renders a small popup near it,
// and lets the user capture the selection into a note.
// This script never talks to the network directly — it always routes
// through the background service worker (chrome.runtime.sendMessage),
// since background scripts have far fewer cross-origin restrictions
// than content scripts injected into arbitrary third-party pages.

(function () {
  const HOST_ID = "flow-notes-popup-host";

  // An earlier instance of this script may still have its popup host in
  // the page — the extension was updated, or the worker re-injected us
  // into an already-open tab. That old instance is orphaned and can no
  // longer reach the worker, so remove its host and take over rather than
  // leaving two popups fighting over the same selection.
  const stale = document.getElementById(HOST_ID);
  if (stale) stale.remove();
  let hostEl = null;
  let shadow = null;
  let currentSelectionText = "";
  let currentSourceInfo = null;
  // Notes from the last list load, keyed by id. A capture needs the note's
  // storage_target and local_path, and the <select> can only carry the id.
  let notesById = new Map();

  // Mirrors the toolbar popup's capture toggle. Read once, then kept in
  // step via storage.onChanged so already-open tabs react immediately
  // instead of needing a reload.
  let captureEnabled = true;

  try {
    chrome.storage.sync.get({ captureEnabled: true }, (stored) => {
      if (!chrome.runtime.lastError) {
        captureEnabled = stored.captureEnabled !== false;
      }
    });
    chrome.storage.onChanged.addListener((changes) => {
      if (!changes.captureEnabled) return;
      captureEnabled = changes.captureEnabled.newValue !== false;
      if (!captureEnabled) hidePopup();
    });
  } catch (err) {
    // Orphaned content script after an extension reload — leave capture
    // on; the send path reports the invalidated context clearly anyway.
    console.debug("[flow-notes] could not read capture setting:", err);
  }

  function ensureHost() {
    if (hostEl) return shadow;
    hostEl = document.createElement("div");
    hostEl.id = HOST_ID;
    // Keep the host element itself out of page layout/flow.
    hostEl.style.position = "absolute";
    hostEl.style.top = "0";
    hostEl.style.left = "0";
    hostEl.style.zIndex = "2147483647"; // max z-index, sit above page content
    document.documentElement.appendChild(hostEl);

    shadow = hostEl.attachShadow({ mode: "open" });
    shadow.innerHTML = `
      <style>
        /* Ruled-paper card, matching the toolbar popup. Everything is a
           CSS gradient — no assets to load, which matters here because
           this renders on every page the user visits.

           :host{all:initial} above means nothing inherits from the host
           page, so every property this card needs is set explicitly. */
        :host { all: initial; }

        .fn-popup {
          --rule: 24px;
          --paper: #f9f3e1;
          --rule-ink: rgba(74, 60, 36, 0.15);
          --ink: #2f2a21;
          --ink-soft: #6d6353;
          --ink-faint: #9a8f7c;
          --surface: #fffdf5;
          --edge: rgba(74, 60, 36, 0.20);
          --highlight: #ffe583;

          position: fixed;
          display: none;
          flex-direction: column;
          gap: 7px;
          width: 258px;
          padding: 11px 12px 10px;
          border-radius: 10px;
          border: 1px solid rgba(72, 60, 40, 0.16);
          color: var(--ink);
          background-color: var(--paper);
          background-image:
            radial-gradient(120% 80% at 50% 0%, rgba(255,255,255,0.6) 0%, transparent 65%),
            repeating-linear-gradient(
              to bottom,
              transparent 0,
              transparent calc(var(--rule) - 1px),
              var(--rule-ink) calc(var(--rule) - 1px),
              var(--rule-ink) var(--rule)
            );
          background-position: 0 0, 0 10px;
          box-shadow: 0 10px 28px rgba(40, 30, 15, 0.18), 0 2px 6px rgba(40, 30, 15, 0.10);
          font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
          font-size: 12px;
          line-height: var(--rule);
        }

        @media (prefers-color-scheme: dark) {
          .fn-popup {
            --paper: #1b1917;
            --rule-ink: rgba(255, 238, 205, 0.07);
            --ink: #ece3d1;
            --ink-soft: #a89d88;
            --ink-faint: #7c7263;
            --surface: #24211d;
            --edge: rgba(255, 238, 205, 0.17);
            --highlight: rgba(255, 219, 120, 0.20);
            border-color: rgba(255, 238, 205, 0.14);
            background-image:
              radial-gradient(120% 80% at 50% 0%, rgba(255,255,255,0.04) 0%, transparent 65%),
              repeating-linear-gradient(
                to bottom,
                transparent 0,
                transparent calc(var(--rule) - 1px),
                var(--rule-ink) calc(var(--rule) - 1px),
                var(--rule-ink) var(--rule)
              );
          }
        }

        .fn-eyebrow {
          font-size: 8px;
          letter-spacing: 0.16em;
          text-transform: uppercase;
          color: var(--ink-faint);
          line-height: 1;
        }

        .fn-row { display: flex; gap: 6px; }

        .fn-select {
          width: 100%;
          appearance: none;
          -webkit-appearance: none;
          font-family: inherit;
          font-size: 11px;
          line-height: 1.2;
          color: var(--ink);
          background-color: var(--surface);
          background-image:
            linear-gradient(45deg, transparent 50%, var(--ink-faint) 50%),
            linear-gradient(135deg, var(--ink-faint) 50%, transparent 50%);
          background-position: right 11px center, right 6px center;
          background-size: 5px 5px, 5px 5px;
          background-repeat: no-repeat;
          border: 1px solid var(--edge);
          border-radius: 6px;
          padding: 6px 22px 6px 8px;
          cursor: pointer;
        }
        .fn-select:hover { border-color: var(--ink-faint); }
        .fn-select option { color: #2f2a21; background: #fffdf5; }

        .fn-btn {
          flex: 1;
          font-family: inherit;
          font-size: 11px;
          font-weight: 500;
          line-height: 1.2;
          color: var(--ink);
          background: var(--surface);
          border: 1px solid var(--edge);
          border-radius: 6px;
          padding: 6px 8px;
          cursor: pointer;
          transition: background-color 120ms ease, border-color 120ms ease;
        }
        .fn-btn:hover { border-color: var(--ink-faint); }

        /* One primary action. Save is what the user came here to do. */
        .fn-btn.fn-primary {
          color: var(--paper);
          background: var(--ink);
          border-color: var(--ink);
        }
        .fn-btn.fn-primary:hover { opacity: 0.88; }

        .fn-status {
          font-size: 10px;
          line-height: 14px;
          min-height: 14px;
          color: var(--ink-soft);
          word-break: break-word;
        }
      </style>
      <div class="fn-popup" id="fn-popup">
        <div class="fn-eyebrow">Save highlight to</div>
        <select class="fn-select" id="fn-note-select">
          <option value="__loading__">Loading notes…</option>
        </select>
        <div class="fn-row">
          <button class="fn-btn fn-primary" id="fn-save-btn">Save</button>
          <button class="fn-btn" id="fn-new-note-btn">New note</button>
        </div>
        <select class="fn-select" id="fn-storage-select" title="Where a new note gets stored">
          <option value="drive">New note in: Google Drive</option>
          <option value="apple_notes">New note in: Apple Notes</option>
          <option value="local">New note in: Local file</option>
        </select>
        <div class="fn-status" id="fn-status"></div>
      </div>
    `;

    hideUnsupportedTargets(shadow.getElementById("fn-storage-select"));

    shadow.getElementById("fn-save-btn").addEventListener("click", handleSave);
    shadow.getElementById("fn-new-note-btn").addEventListener("click", handleCreateNote);

    return shadow;
  }

  function showPopup(rect) {
    const sh = ensureHost();
    const popup = sh.getElementById("fn-popup");
    popup.style.display = "flex";

    // Position above the selection, centered horizontally.
    // We measure after display:flex so offsetWidth/offsetHeight are accurate.
    requestAnimationFrame(() => {
      const popupRect = popup.getBoundingClientRect();
      let top = rect.top - popupRect.height - 8;
      let left = rect.left + rect.width / 2 - popupRect.width / 2;

      // If there's no room above, place it below the selection instead.
      if (top < 8) top = rect.bottom + 8;
      // Keep it on-screen horizontally.
      left = Math.max(8, Math.min(left, window.innerWidth - popupRect.width - 8));

      popup.style.top = `${top}px`;
      popup.style.left = `${left}px`;
    });

    loadNotesList();
  }

  function hidePopup() {
    if (!shadow) return;
    const popup = shadow.getElementById("fn-popup");
    popup.style.display = "none";
  }

  // Apple Notes only exists on macOS. Drop the option elsewhere so the
  // user is never offered a target that can only produce an error.
  async function hideUnsupportedTargets(select) {
    if (!select || !extensionContextValid()) return;
    try {
      const response = await sendMessage({ type: "GET_PLATFORM" });
      if (response.os === "mac") return;
      const option = select.querySelector('option[value="apple_notes"]');
      if (option) option.remove();
    } catch (err) {
      // Not worth surfacing: leaving the option in place just means the
      // server explains the problem if it's actually picked.
      console.debug("[flow-notes] platform check failed:", err);
    }
  }

  function setStatus(msg) {
    if (!shadow) return;
    shadow.getElementById("fn-status").textContent = msg || "";
  }

  // A message round-trip can hang indefinitely: the MV3 service worker can
  // be terminated mid-request without ever calling sendResponse, and
  // getAuthToken({interactive:true}) blocks for as long as an auth window
  // stays unresolved. Without a ceiling the popup just sits on
  // "Creating note…", which tells the user nothing at all.
  const MESSAGE_TIMEOUT_MS = 30000;

  // Reloading the extension orphans content scripts already injected into
  // open tabs: the script keeps running but loses its channel to the
  // worker, and chrome.runtime goes away mid-flight. Detect it and say
  // what to do, rather than surfacing "Cannot read properties of
  // undefined (reading 'sendMessage')".
  function extensionContextValid() {
    try {
      return Boolean(chrome && chrome.runtime && chrome.runtime.id);
    } catch {
      return false;
    }
  }

  function sendMessage(message) {
    return new Promise((resolve, reject) => {
      let settled = false;

      if (!extensionContextValid()) {
        reject(
          new Error("extension was reloaded — refresh this page and try again.")
        );
        return;
      }

      const timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        reject(
          new Error(
            "timed out after 30s with no reply. Open chrome://extensions " +
              "→ Flow Notes → service worker for the real error."
          )
        );
      }, MESSAGE_TIMEOUT_MS);

      chrome.runtime.sendMessage(message, (response) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);

        if (chrome.runtime.lastError) {
          reject(new Error(chrome.runtime.lastError.message));
        } else if (!response) {
          reject(new Error("no response from the background worker."));
        } else if (!response.ok) {
          reject(new Error(response.error || "unknown error."));
        } else {
          resolve(response);
        }
      });
    });
  }

  function loadNotesList() {
    if (!extensionContextValid()) {
      const select = shadow.getElementById("fn-note-select");
      select.innerHTML = "";
      const opt = document.createElement("option");
      opt.value = "__error__";
      opt.textContent = "Extension reloaded — refresh this page";
      select.appendChild(opt);
      return;
    }

    chrome.runtime.sendMessage({ type: "GET_NOTES" }, (response) => {
      const select = shadow.getElementById("fn-note-select");
      select.innerHTML = "";
      notesById = new Map();

      if (chrome.runtime.lastError || !response || !response.ok) {
        const opt = document.createElement("option");
        opt.value = "__error__";
        opt.textContent = "Could not load notes";
        select.appendChild(opt);
        return;
      }

      if (!response.notes || response.notes.length === 0) {
        const opt = document.createElement("option");
        opt.value = "__none__";
        opt.textContent = "No notes yet — create one";
        select.appendChild(opt);
        return;
      }

      for (const note of response.notes) {
        notesById.set(note.id, note);
        const opt = document.createElement("option");
        opt.value = note.id;
        opt.textContent = `${note.title} (${note.storage_target})`;
        select.appendChild(opt);
      }
    });
  }

  async function handleSave() {
    const select = shadow.getElementById("fn-note-select");
    const noteId = select.value;

    if (!noteId || noteId.startsWith("__")) {
      setStatus("Pick or create a note first.");
      return;
    }

    setStatus("Saving…");

    try {
      const note = notesById.get(noteId);
      await sendMessage({
        type: "SAVE_CAPTURE",
        payload: {
          note_id: noteId,
          text: currentSelectionText,
          source_url: currentSourceInfo.url,
          source_title: currentSourceInfo.title,
          timestamp: new Date().toISOString(),
          storage_target: note ? note.storage_target : undefined,
          local_path: note ? note.local_path : undefined,
          apple_note_id: note ? note.apple_note_id : undefined,
        },
      });
    } catch (err) {
      console.error("[flow-notes] save failed:", err);
      // Nothing was stored anywhere, so leave the popup open with the
      // selection still live rather than implying a partial save.
      setStatus("Not saved: " + err.message);
      return;
    }

    setStatus("Saved ✓");
    setTimeout(hidePopup, 700);
  }

  async function handleCreateNote() {
    const title = window.prompt("New note title:");
    if (!title) return;

    const storageTarget = shadow.getElementById("fn-storage-select").value;

    const creatingLabel = {
      local: "Creating local note…",
      apple_notes: "Creating note in Apple Notes…",
    };
    setStatus(creatingLabel[storageTarget] || "Creating note…");
    try {
      await sendMessage({
        type: "CREATE_NOTE",
        payload: { title, storage_target: storageTarget },
      });
    } catch (err) {
      console.error("[flow-notes] create note failed:", err);
      setStatus("Could not create note: " + err.message);
      return;
    }
    setStatus("Note created.");
    loadNotesList();
  }

  function onSelectionChange() {
    if (!captureEnabled) {
      hidePopup();
      return;
    }

    const selection = window.getSelection();
    const text = selection ? selection.toString().trim() : "";

    if (!text) {
      hidePopup();
      return;
    }

    currentSelectionText = text;
    currentSourceInfo = { url: window.location.href, title: document.title };

    const range = selection.getRangeAt(0);
    const rect = range.getBoundingClientRect();
    showPopup(rect);
  }

  document.addEventListener("mouseup", onSelectionChange);
  document.addEventListener("keyup", (e) => {
    // Support keyboard-driven selection (shift+arrow keys).
    if (e.shiftKey) onSelectionChange();
  });

  // Hide popup when clicking elsewhere.
  document.addEventListener("mousedown", (e) => {
    if (hostEl && !e.composedPath().includes(hostEl)) {
      hidePopup();
    }
  });

  // Reposition/hide on scroll to avoid a stale floating popup.
  window.addEventListener("scroll", () => hidePopup(), true);
})();
