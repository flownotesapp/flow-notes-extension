// background.js (MV3 service worker)
// Owns: Google auth token retrieval, all fetch() calls to our backend
// and to localhost (local server mode), and message routing between
// the content script / popup UI and the outside world.
//
// NOTE: BACKEND_URL and LOCAL_SERVER_URL are placeholders for now —
// wire these up once the Go backend exists.

const BACKEND_URL = "https://flow-notes-backend-production.up.railway.app";

// A URL without a scheme is not an error to fetch() — it is treated as a
// path relative to the extension's own origin, so the request quietly goes
// to chrome-extension://<id>/... and fails as an unhelpful "Failed to
// fetch". Catch that here instead.
if (!/^https?:\/\//.test(BACKEND_URL)) {
  console.error(
    `[flow-notes] BACKEND_URL is missing its scheme: "${BACKEND_URL}". ` +
      `It must start with https:// (or http:// for local development).`
  );
}

async function getAuthToken() {
  return new Promise((resolve, reject) => {
    chrome.identity.getAuthToken({ interactive: true }, (token) => {
      if (chrome.runtime.lastError || !token) {
        reject(chrome.runtime.lastError || new Error("No token"));
        return;
      }
      resolve(token);
    });
  });
}

async function removeCachedAuthToken(token) {
  return new Promise((resolve) => {
    chrome.identity.removeCachedAuthToken({ token }, () => resolve());
  });
}

// The backend now verifies bearer tokens against Google's tokeninfo
// endpoint, so an expired token comes back as a 401 rather than being
// silently accepted. chrome.identity caches tokens and will happily hand
// back one that has already expired, so on a 401 we evict the cached copy
// and retry once with a freshly minted token.
async function apiFetch(path, options = {}, { allowRetry = true } = {}) {
  // The backend requires a verified Google token, so a sign-in failure is
  // fatal — report it as itself rather than sending an anonymous request
  // and reporting the 401 that follows.
  let token;
  try {
    token = await getAuthToken();
  } catch (err) {
    throw new Error(`Google sign-in failed: ${err && err.message ? err.message : err}`);
  }

  const headers = {
    "Content-Type": "application/json",
    Authorization: `Bearer ${token}`,
    ...(options.headers || {}),
  };

  let res;
  try {
    res = await fetch(`${BACKEND_URL}${path}`, { ...options, headers });
  } catch (err) {
    // fetch() rejects on DNS failure, connection refused, TLS errors and
    // CORS rejections alike, with no detail — name the URL so the cause is
    // at least locatable.
    throw new Error(
      `Could not reach the backend at ${BACKEND_URL}${path}. ` +
        `Check BACKEND_URL, that the server is running, and that its ` +
        `ALLOWED_ORIGINS includes this extension. (${err})`
    );
  }

  if (res.status === 401 && allowRetry) {
    await removeCachedAuthToken(token);
    return apiFetch(path, options, { allowRetry: false });
  }

  if (!res.ok) {
    // The backend replies {"error": "..."} — that message is far more
    // useful than the bare status code.
    const detail = await res.text().catch(() => "");
    let message = detail;
    try {
      const parsed = JSON.parse(detail);
      if (parsed && parsed.error) message = parsed.error;
    } catch {
      // Not JSON (a proxy error page, say) — fall through to the raw text.
    }
    throw new Error(`Backend error ${res.status}: ${message || res.statusText}`);
  }
  return res.json();
}

// Chrome launches the helper on demand and talks to it over stdio, so
// there is no port, no CORS, no Private Network Access preflight, and
// nothing for the user to start. Chrome enforces access through the host
// manifest's allowed_origins rather than an Origin header.
const HELPER_HOST = "com.flownotes.local";
let helperMessageId = 0;

async function callHelper(method, params = {}) {
  let reply;
  try {
    reply = await chrome.runtime.sendNativeMessage(HELPER_HOST, {
      id: ++helperMessageId,
      method,
      params,
    });
  } catch (err) {
    // Chrome rejects here when the host manifest is missing, names a
    // binary that isn't there, or doesn't list this extension.
    const detail = err && err.message ? err.message : String(err);
    throw new Error(
      "The Flow Notes helper isn't available. Install it and restart " +
        `Chrome — see the setup steps in the popup. (${detail})`
    );
  }

  if (!reply) {
    throw new Error("The helper sent no reply.");
  }
  if (!reply.ok) {
    // The helper classifies failures so the extension doesn't have to
    // guess: "unsupported" is a platform limit, "forbidden" is macOS
    // withholding automation permission.
    throw new Error(reply.error || "The helper reported an unknown failure.");
  }
  return reply;
}

// The oldest helper this build can work with. Bump it whenever the
// extension starts calling an endpoint the helper did not previously
// have — otherwise a stale helper fails in ways that look like extension
// bugs rather than a missing update.
const HELPER_MIN_VERSION = "0.6.0";

// Set this to your GitHub Releases page once the first release is cut.
// Until then the setup panel says the download isn't published rather than
// offering a dead link — a placeholder that silently does nothing when
// clicked is worse than one that admits it isn't configured.
const HELPER_DOWNLOAD_URL =
  "https://github.com/saumya-vyas/flow-notes-extension/releases/latest";

const HELPER_DOWNLOAD_CONFIGURED = !HELPER_DOWNLOAD_URL.includes("YOUR-USER");

// compareVersions returns -1, 0 or 1. Numeric part-by-part, so "0.10.0"
// sorts above "0.9.0" — string comparison gets that backwards.
function compareVersions(a, b) {
  const parse = (v) =>
    String(v || "0")
      .split(".")
      .map((n) => parseInt(n, 10) || 0);
  const left = parse(a);
  const right = parse(b);

  for (let i = 0; i < Math.max(left.length, right.length); i++) {
    const l = left[i] || 0;
    const r = right[i] || 0;
    if (l !== r) return l < r ? -1 : 1;
  }
  return 0;
}

const CAPTURE_KEY = "captureEnabled";

// A user's own Groq key. Kept in storage.sync so it follows their Chrome
// profile, sent with organize requests as a header, and never written to
// a log or a note. Without it the server falls back to its own key, whose
// free-tier quota is shared by everyone.
const GROQ_KEY = "groqApiKey";
const HEADER_USER_KEY = "X-Groq-Key";

async function userModelKey() {
  try {
    const stored = await chrome.storage.sync.get({ [GROQ_KEY]: "" });
    return String(stored[GROQ_KEY] || "").trim();
  } catch (err) {
    console.debug("[flow-notes] could not read the stored API key:", err);
    return "";
  }
}

// A badge on the toolbar icon, so a paused extension is visible without
// opening the popup — otherwise "why isn't the highlight popup showing?"
// has no answer on the page itself.
async function refreshCaptureBadge() {
  try {
    const stored = await chrome.storage.sync.get({ [CAPTURE_KEY]: true });
    const paused = stored[CAPTURE_KEY] === false;
    await chrome.action.setBadgeBackgroundColor({ color: "#9e332d" });
    await chrome.action.setBadgeText({ text: paused ? "off" : "" });
  } catch (err) {
    console.error("[flow-notes] could not update badge:", err);
  }
}

chrome.runtime.onInstalled.addListener(async () => {
  await refreshCaptureBadge();
  await injectIntoOpenTabs();
});

// Chrome only auto-injects content scripts into pages loaded AFTER the
// extension is installed or updated. Without this, a brand-new user
// installs, highlights something on the page they were already reading,
// and nothing happens at all — with nothing to tell them a reload is
// needed. It also covers updates, where the previous content script is
// orphaned and loses its channel to this worker.
async function injectIntoOpenTabs() {
  const contentScripts = chrome.runtime.getManifest().content_scripts;
  if (!contentScripts || !contentScripts.length) return;
  const files = contentScripts[0].js;

  let tabs = [];
  try {
    tabs = await chrome.tabs.query({ url: ["http://*/*", "https://*/*"] });
  } catch (err) {
    console.warn("[flow-notes] could not enumerate tabs:", err);
    return;
  }

  await Promise.all(
    tabs.map(async (tab) => {
      if (!tab.id) return;
      try {
        await chrome.scripting.executeScript({
          target: { tabId: tab.id },
          files,
        });
      } catch (err) {
        // Chrome refuses injection into its own pages, the Web Store and
        // the PDF viewer. Expected for some tabs, not worth reporting.
        console.debug("[flow-notes] skipped tab", tab.id, err && err.message);
      }
    })
  );
}
chrome.runtime.onStartup.addListener(refreshCaptureBadge);
chrome.storage.onChanged.addListener((changes) => {
  if (changes[CAPTURE_KEY]) refreshCaptureBadge();
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  handleMessage(message)
    .then((data) => sendResponse({ ok: true, ...data }))
    .catch((err) => {
      console.error("[flow-notes] background error:", err);
      sendResponse({ ok: false, error: String(err) });
    });
  // Return true to indicate we'll respond asynchronously.
  return true;
});

async function handleMessage(message) {
  switch (message.type) {
    case "GET_NOTES": {
      const data = await apiFetch("/api/notes", { method: "GET" });
      return { notes: data.notes };
    }

    case "CREATE_NOTE": {
      const { title, storage_target = "drive" } = message.payload;

      // For local notes the file is created FIRST, then registered with the
      // backend. The other order would risk a note row pointing at a file
      // that was never created; this way a failure at the second step
      // leaves only a stray Markdown file, which is harmless and visible.
      let localPath;
      let appleNoteID;
      if (storage_target === "local") {
        const created = await callHelper("note.create", { title });
        localPath = created.path;
      } else if (storage_target === "apple_notes") {
        const created = await callHelper("apple.create", { title });
        appleNoteID = created.id;
      }

      const data = await apiFetch("/api/notes", {
        method: "POST",
        // The backend rejects an external reference that doesn't match the
        // target, so these are omitted rather than sent empty.
        body: JSON.stringify({
          title,
          storage_target,
          local_path: localPath,
          apple_note_id: appleNoteID,
        }),
      });
      return { note: data.note };
    }

    case "SAVE_CAPTURE": {
      const {
        note_id,
        text,
        source_url,
        source_title,
        timestamp,
        storage_target,
        local_path,
        apple_note_id,
      } = message.payload;

      // The server keeps no copy of captured text, so the note itself is
      // the only place it will ever live. That means a failed write loses
      // the highlight — so these throw rather than reporting partial
      // success, and the popup keeps the selection recoverable by telling
      // the user it did not save.
      if (storage_target === "local" || storage_target === "apple_notes") {
        if (storage_target === "local") {
          if (!local_path) throw new Error("this note has no local file path recorded");
          await callHelper("note.append", { path: local_path, text, source_url, source_title });
        } else {
          if (!apple_note_id) throw new Error("this note has no Apple Notes id recorded");
          await callHelper("apple.append", { id: apple_note_id, text, source_url, source_title });
        }
        // Written locally; this only refreshes the note's place in the
        // list, so a failure here is not a lost capture.
        try {
          await apiFetch(`/api/notes/${note_id}/captures`, {
            method: "POST",
            body: JSON.stringify({ text: "", source_url, source_title, timestamp }),
          });
        } catch (err) {
          console.debug("[flow-notes] could not refresh note order:", err);
        }
        return { saved: true };
      }

      // Drive notes are written by the backend, straight into the Doc.
      await apiFetch(`/api/notes/${note_id}/captures`, {
        method: "POST",
        body: JSON.stringify({ text, source_url, source_title, timestamp }),
      });
      return { saved: true };
    }

    case "DELETE_NOTE": {
      const { note_id, storage_target, local_path, apple_note_id } = message.payload;

      // Content first: if trashing the local copy fails, the metadata row
      // stays so the user can retry, rather than orphaning a file the
      // extension can no longer name.
      if (storage_target === "local" && local_path) {
        await callHelper("note.delete", { path: local_path });
      } else if (storage_target === "apple_notes" && apple_note_id) {
        await callHelper("apple.delete", { id: apple_note_id });
      }

      // For Drive notes the backend trashes the Doc itself before dropping
      // the row, for the same reason.
      await apiFetch(`/api/notes/${note_id}`, { method: "DELETE" });
      return { deleted: true };
    }

    // Is the local helper installed and current? Answers with a result
    // rather than throwing: "not installed" is a normal state for anyone
    // using Drive-backed notes, not an error.
    case "LOCAL_SERVER_STATUS": {
      const base = {
        downloadUrl: HELPER_DOWNLOAD_CONFIGURED ? HELPER_DOWNLOAD_URL : "",
        downloadConfigured: HELPER_DOWNLOAD_CONFIGURED,
        requiredVersion: HELPER_MIN_VERSION,
        extensionId: chrome.runtime.id,
      };

      try {
        const info = await callHelper("health");
        const version = info.version || "0.0.0";
        return {
          ...base,
          reachable: true,
          version,
          notesDir: info.dir,
          appleNotes: info.apple_notes,
          outdated: compareVersions(version, HELPER_MIN_VERSION) < 0,
        };
      } catch (err) {
        return {
          ...base,
          reachable: false,
          reason: err && err.message ? err.message : String(err),
        };
      }
    }

    // Content scripts can't call chrome.runtime.getPlatformInfo, so the
    // popups ask the worker instead. Used to hide storage targets the
    // platform can't support rather than offering a choice that fails.
    // Opening a Markdown note means handing it to the OS, which only the
    // helper can do — same reason Apple notes go through it.
    case "OPEN_LOCAL_NOTE": {
      const { local_path, reveal } = message.payload;
      if (!local_path) throw new Error("this note has no local file path recorded");
      await callHelper("note.open", { path: local_path, reveal: Boolean(reveal) });
      return { opened: true };
    }

    // The popup only ever learns whether a key is set, never its value —
    // there is no reason to move a credential around for a checkbox.
    case "GET_MODEL_KEY_STATUS": {
      const key = await userModelKey();
      return { hasKey: Boolean(key), hint: key ? key.slice(0, 4) + "…" + key.slice(-4) : "" };
    }

    case "SET_MODEL_KEY": {
      const value = String(message.payload.key || "").trim();
      if (value) {
        await chrome.storage.sync.set({ [GROQ_KEY]: value });
      } else {
        await chrome.storage.sync.remove(GROQ_KEY);
      }
      return { hasKey: Boolean(value) };
    }

    case "DELETE_ALL_DATA": {
      await apiFetch("/api/user/data", { method: "DELETE" });
      return { deleted: true };
    }

    case "GET_PLATFORM": {
      const info = await chrome.runtime.getPlatformInfo();
      return { os: info.os };
    }

    // Opening an Apple note has to go through the helper: an extension
    // can't script Notes.app, but the process on the user's machine can.
    case "OPEN_APPLE_NOTE": {
      const { apple_note_id } = message.payload;
      if (!apple_note_id) throw new Error("this note has no Apple Notes id recorded");
      await callHelper("apple.open", { id: apple_note_id });
      return { opened: true };
    }

    // The whole organize flow lives here rather than in the popup, because
    // a popup closes the moment it loses focus. For Apple Notes and local
    // files the backend can only *render* the new content — something on
    // this machine has to write it — so if the second step lived in the
    // popup, clicking away mid-run would leave the note un-updated with no
    // indication why.
    case "ORGANIZE_NOTE": {
      const { note_id, mode, storage_target, local_path, apple_note_id } = message.payload;

      // The note is now the only copy of its content, so for targets the
      // backend cannot reach we have to read it here and send it up.
      let content;
      if (storage_target === "local") {
        if (!local_path) throw new Error("this note has no local file path recorded");
        content = (await callHelper("note.read", { path: local_path })).content;
      } else if (storage_target === "apple_notes") {
        if (!apple_note_id) throw new Error("this note has no Apple Notes id recorded");
        // HTML, not plaintext: plaintext drops the href from every
        // [source] link, which would sever the citations.
        content = (await callHelper("apple.read", { id: apple_note_id })).html;
      }

      const key = await userModelKey();
      const result = await apiFetch(`/api/notes/${note_id}/organize`, {
        method: "POST",
        headers: key ? { [HEADER_USER_KEY]: key } : {},
        body: JSON.stringify({ mode, content }),
      });

      switch (result.format) {
        case "drive":
          break; // the backend read and rewrote the Doc itself

        case "html":
          await callHelper("apple.write", { id: apple_note_id, html: result.content });
          break;

        case "markdown":
          await callHelper("note.write", { path: local_path, content: result.content });
          break;

        default:
          throw new Error(`unexpected organize format "${result.format}"`);
      }

      return { mode: result.mode, storage_target: storage_target || result.storage_target };
    }

    default:
      throw new Error(`Unknown message type: ${message.type}`);
  }
}
