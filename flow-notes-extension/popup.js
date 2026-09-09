// popup.js
// Renders the notes list. Clicking a note opens it:
//  - Drive-backed notes -> open the Google Doc in a new tab
//  - Local-file-backed notes -> we can't "open" a local file from
//    a Chrome extension directly (no filesystem access), so for now
//    we just show the file path; opening it is left to the user's
//    local server /or their file manager. Revisit once local mode
//    is built out.

// Pausing capture is a preference, so it lives in storage.sync and
// follows the user's Chrome profile. It does NOT disable the extension
// itself — that would need the "management" permission and would be a
// one-way door, since a disabled extension can't re-enable itself.
const CAPTURE_KEY = "captureEnabled";

document.addEventListener("DOMContentLoaded", loadNotes);
document.addEventListener("DOMContentLoaded", initCaptureToggle);
document.addEventListener("DOMContentLoaded", initSetupPanel);
document.addEventListener("DOMContentLoaded", initSettings);

// --- settings ----------------------------------------------------------

function initSettings() {
  const toggle = document.getElementById("settings-toggle");
  const panel = document.getElementById("settings-panel");

  toggle.addEventListener("click", () => {
    panel.hidden = !panel.hidden;
    toggle.textContent = panel.hidden ? "Settings" : "Hide settings";
    if (!panel.hidden) refreshKeyStatus();
  });

  document.getElementById("groq-save").addEventListener("click", () => {
    const input = document.getElementById("groq-key");
    const key = input.value.trim();
    if (!key) {
      setKeyStatus("Paste a key first, or use Clear to remove the saved one.");
      return;
    }
    saveKey(key, () => {
      // Never leave a credential sitting in a visible field.
      input.value = "";
      setKeyStatus("Saved. Organise and Summarise will use your key.");
    });
  });

  document.getElementById("groq-clear").addEventListener("click", () => {
    document.getElementById("groq-key").value = "";
    saveKey("", () => setKeyStatus("Cleared. Falling back to the server's shared key."));
  });

  initDeleteAll();
}

function saveKey(key, done) {
  chrome.runtime.sendMessage({ type: "SET_MODEL_KEY", payload: { key } }, (response) => {
    if (responseError(response)) {
      setKeyStatus("Could not save the key.");
      return;
    }
    done();
  });
}

function refreshKeyStatus() {
  chrome.runtime.sendMessage({ type: "GET_MODEL_KEY_STATUS" }, (response) => {
    if (responseError(response)) return;
    // Only ever the first and last few characters: enough to recognise
    // which key is set, useless to anyone reading over a shoulder.
    setKeyStatus(
      response.hasKey
        ? `A key is saved (${response.hint}).`
        : "No key saved — using the server's shared key, whose limits are shared by everyone."
    );
  });
}

function setKeyStatus(text) {
  document.getElementById("groq-status").textContent = text;
}

// Two-click confirm, like note deletion: this is not recoverable.
function initDeleteAll() {
  const btn = document.getElementById("delete-all");
  const status = document.getElementById("delete-status");
  let armed = false;
  let timer = null;

  const disarm = () => {
    armed = false;
    clearTimeout(timer);
    btn.classList.remove("confirm");
    btn.textContent = "Delete my data from the server";
  };

  btn.addEventListener("click", () => {
    if (!armed) {
      armed = true;
      btn.classList.add("confirm");
      btn.textContent = "Click again to delete everything";
      status.textContent =
        "This erases every note record this server holds for you. Your Google Docs, " +
        "Apple notes and Markdown files are untouched — they were never stored there.";
      timer = setTimeout(disarm, 5000);
      return;
    }

    disarm();
    btn.disabled = true;
    status.textContent = "Deleting…";

    chrome.runtime.sendMessage({ type: "DELETE_ALL_DATA" }, (response) => {
      btn.disabled = false;
      const err = responseError(response);
      if (err) {
        status.textContent = "Could not delete: " + err;
        return;
      }
      status.textContent = "Deleted. Your notes themselves are still where they were.";
      loadNotes();
    });
  });
}

// --- local helper setup ------------------------------------------------
//
// Apple Notes and local files need a helper program on this machine;
// Drive-backed notes do not. So the panel appears only when local storage
// is actually in play — either a local-target note exists, or the picker
// is set to one. Nobody using Drive is ever nagged to install anything.

const LOCAL_TARGETS = ["local", "apple_notes"];
let loadedNotes = [];
let helperStatus = null;

function initSetupPanel() {
  document.getElementById("setup-recheck").addEventListener("click", () => {
    document.getElementById("setup-recheck").textContent = "Checking…";
    refreshHelperStatus();
  });
  document
    .getElementById("storage-target")
    .addEventListener("change", updateSetupPanel);
  refreshHelperStatus();
}

function refreshHelperStatus() {
  chrome.runtime.sendMessage({ type: "LOCAL_SERVER_STATUS" }, (response) => {
    helperStatus = responseError(response) ? { reachable: false } : response;
    document.getElementById("setup-recheck").textContent = "Check again";
    updateSetupPanel();
  });
}

function localStorageInUse() {
  const picked = document.getElementById("storage-target").value;
  if (LOCAL_TARGETS.includes(picked)) return true;
  return loadedNotes.some((n) => LOCAL_TARGETS.includes(n.storage_target));
}

function updateSetupPanel() {
  const panel = document.getElementById("setup-panel");
  if (!helperStatus) return;

  const healthy = helperStatus.reachable && !helperStatus.outdated;
  if (healthy || !localStorageInUse()) {
    panel.hidden = true;
    return;
  }

  const title = document.getElementById("setup-title");
  const body = document.getElementById("setup-body");
  const steps = document.getElementById("setup-steps");
  const download = document.getElementById("setup-download");

  steps.innerHTML = "";

  // No release published yet: don't offer a link that goes nowhere.
  const canDownload = Boolean(helperStatus.downloadConfigured && helperStatus.downloadUrl);
  download.hidden = !canDownload;
  if (canDownload) {
    download.href = helperStatus.downloadUrl;
  }

  if (helperStatus.reachable && helperStatus.outdated) {
    title.textContent = "Update the local helper";
    body.textContent =
      `The helper on this Mac is version ${helperStatus.version}, but this ` +
      `version of the extension needs ${helperStatus.requiredVersion} or newer. ` +
      `Download it and run the installer again, then restart Chrome.`;
    download.textContent = "Download update";
    if (!canDownload) {
      addStep(steps, "No release has been published yet. Re-run ./install.sh from your flow-notes-local folder.");
    }
  } else {
    title.textContent = "Set up local storage";
    body.textContent =
      "Apple Notes and local files are written by a small helper program on " +
      "this Mac, which Chrome starts for you when it's needed. Notes stored " +
      "in Google Drive don't need it.";
    download.textContent = "Download helper";

    // Show the exact command with this installation's real extension id,
    // so nobody has to go and find it on chrome://extensions.
    if (canDownload) {
      addStep(steps, "Unzip the download, then run this in Terminal from that folder:");
    } else {
      addStep(
        steps,
        "No release has been published yet. Run this from your " +
          "flow-notes-local folder instead:"
      );
    }
    addStep(
      steps,
      "",
      `./install.sh chrome-extension://${helperStatus.extensionId || chrome.runtime.id}`
    );
    addStep(steps, "Restart Chrome. There's nothing to keep running — Chrome starts the helper when it's needed.");
    addStep(
      steps,
      "macOS may say the developer can't be verified — this build isn't signed. " +
        "The installer handles it; if macOS still blocks it, right-click the file and choose Open."
    );
    addStep(
      steps,
      "Your first Apple Notes capture asks permission to control Notes. The dialog names " +
        "Google Chrome, since Chrome launches the helper. Approve it once."
    );
  }
  panel.hidden = false;
}

function addStep(list, text, code) {
  const li = document.createElement("li");
  if (text) li.appendChild(document.createTextNode(text));
  if (code) {
    const el = document.createElement("code");
    el.textContent = code;
    // Click to copy: this line is long and retyping it invites mistakes.
    el.title = "Click to copy";
    el.style.cursor = "pointer";
    el.addEventListener("click", () => {
      navigator.clipboard.writeText(code).then(
        () => showNotice("Command copied."),
        () => {}
      );
    });
    li.appendChild(el);
  }
  list.appendChild(li);
}

function initCaptureToggle() {
  const button = document.getElementById("power-toggle");
  const label = document.getElementById("power-label");

  const render = (on) => {
    button.setAttribute("aria-checked", String(on));
    label.textContent = on ? "On" : "Off";
  };

  chrome.storage.sync.get({ [CAPTURE_KEY]: true }, (stored) => {
    // Default to on if storage is unreadable: a broken read should not
    // silently stop capture.
    render(chrome.runtime.lastError ? true : stored[CAPTURE_KEY] !== false);
  });

  button.addEventListener("click", () => {
    const next = button.getAttribute("aria-checked") !== "true";
    render(next);
    chrome.storage.sync.set({ [CAPTURE_KEY]: next });
    showNotice(
      next
        ? "Capture on — highlight text on any page to save it."
        : "Capture paused. Your notes are still here."
    );
    setTimeout(clearError, 2500);
  });
}
document.addEventListener("DOMContentLoaded", hideUnsupportedTargets);

// Apple Notes only exists on macOS. Drop the option elsewhere so the user
// is never offered a target that can only produce an error.
function hideUnsupportedTargets() {
  chrome.runtime.sendMessage({ type: "GET_PLATFORM" }, (response) => {
    if (chrome.runtime.lastError || !response || !response.ok) return;
    if (response.os === "mac") return;
    const option = document.querySelector(
      '#storage-target option[value="apple_notes"]'
    );
    if (option) option.remove();
  });
}

// The background worker replies { ok: false, error } on failure. Surfacing
// that verbatim matters — a generic "something went wrong" makes a
// misconfigured backend URL, a rejected token and a database outage all
// look identical.
function showError(message) {
  showBanner(message, false);
}

// Informational messages share the banner but not its alarming styling.
function showNotice(message) {
  showBanner(message, true);
}

function showBanner(message, isNotice) {
  const el = document.getElementById("error-state");
  el.textContent = message;
  el.classList.toggle("notice", isNotice);
  el.style.display = "block";
}

function clearError() {
  const el = document.getElementById("error-state");
  el.textContent = "";
  el.style.display = "none";
}

// chrome.runtime.lastError must be read inside the callback or Chrome logs
// it as an unchecked error; it fires when the worker throws before
// replying at all.
function responseError(response) {
  if (chrome.runtime.lastError) return chrome.runtime.lastError.message;
  if (!response) return "No response from the extension's background worker.";
  if (!response.ok) return response.error || "Unknown error.";
  return null;
}
document.getElementById("new-note-btn").addEventListener("click", createNote);

function loadNotes() {
  chrome.runtime.sendMessage({ type: "GET_NOTES" }, (response) => {
    const listEl = document.getElementById("note-list");
    const emptyEl = document.getElementById("empty-state");
    // #empty-state is a child of the ruled writing area so it sits on a
    // line — clear only the rendered rows, not the container.
    listEl.querySelectorAll(".note-item").forEach((el) => el.remove());

    const err = responseError(response);
    if (err) {
      // A failed load is not an empty list — say which one it is.
      emptyEl.style.display = "none";
      showError("Couldn't load notes:\n" + err);
      return;
    }
    clearError();

    // Set before updateSetupPanel: whether the setup slip is needed
    // depends on whether any note uses a local storage target.
    loadedNotes = response.notes || [];
    updateSetupPanel();

    if (loadedNotes.length === 0) {
      emptyEl.style.display = "block";
      return;
    }
    emptyEl.style.display = "none";

    for (const note of loadedNotes) {
      const item = document.createElement("div");
      item.className = "note-item";

      const title = document.createElement("span");
      title.className = "note-title";
      title.textContent = note.title;

      const target = document.createElement("span");
      target.className = "note-target";
      target.textContent = {
        drive: "Drive",
        apple_notes: "Notes",
        local: "Local",
      }[note.storage_target] || note.storage_target;

      const actions = document.createElement("span");
      actions.className = "note-actions";
      actions.appendChild(aiButton(note, "low", "Organise"));
      actions.appendChild(aiButton(note, "high", "Summarise"));
      actions.appendChild(deleteButton(note));

      item.appendChild(title);
      item.appendChild(target);
      item.appendChild(actions);
      item.addEventListener("click", () => openNote(note));

      listEl.appendChild(item);
    }
  });
}

// Organise (light) and Summarise (heavy) both regenerate the note from its
// raw capture log, which is why re-running one is always safe.
const AI_RUNNING_LABEL = {
  low: "Organising",
  high: "Summarising",
};

function aiButton(note, mode, label) {
  const btn = document.createElement("button");
  btn.className = "note-action";
  btn.textContent = label;
  btn.title =
    mode === "low"
      ? `Organise "${note.title}" — structure and formatting, wording mostly kept`
      : `Summarise "${note.title}" — clarify hard terms, add context only where thin`;

  btn.addEventListener("click", (event) => {
    // Without this the row's own handler opens the note as well.
    event.stopPropagation();
    runAI(note, mode, label);
  });
  return btn;
}

function runAI(note, mode, label) {
  const listEl = document.getElementById("note-list");
  // Lock the whole list, not just this button: a second pass while one is
  // in flight would race to rewrite the same note.
  listEl.classList.add("note-list-busy");
  showNotice(`${AI_RUNNING_LABEL[mode]} "${note.title}"… this can take a minute.`);

  chrome.runtime.sendMessage(
    {
      type: "ORGANIZE_NOTE",
      payload: {
        note_id: note.id,
        mode,
        storage_target: note.storage_target,
        local_path: note.local_path,
        apple_note_id: note.apple_note_id,
      },
    },
    (response) => {
      listEl.classList.remove("note-list-busy");

      const err = responseError(response);
      if (err) {
        showError(`${label} failed for "${note.title}":\n${err}`);
        return;
      }
      showNotice(`Done — open "${note.title}" to read it.`);
      setTimeout(clearError, 4000);
      loadNotes();
    }
  );
}

// Two-click delete: the first click arms the button, the second commits.
// This replaces a confirm() dialog, which can dismiss the extension popup
// out from under itself — and it keeps a destructive action from being one
// stray click away.
function deleteButton(note) {
  const btn = document.createElement("button");
  btn.className = "note-delete";
  btn.textContent = "×";
  btn.title = `Delete "${note.title}"`;
  btn.setAttribute("aria-label", `Delete ${note.title}`);

  let armed = false;
  let disarmTimer = null;

  const disarm = () => {
    armed = false;
    clearTimeout(disarmTimer);
    btn.classList.remove("confirm");
    btn.textContent = "×";
  };

  btn.addEventListener("click", (event) => {
    // Without this the row's own click handler opens the note.
    event.stopPropagation();

    if (!armed) {
      armed = true;
      btn.classList.add("confirm");
      btn.textContent = "Delete?";
      disarmTimer = setTimeout(disarm, 4000);
      showNotice(
        {
          drive: `"${note.title}" will be moved to your Google Drive trash. Click again to confirm.`,
          apple_notes: `"${note.title}" will be moved to Notes' Recently Deleted. Click again to confirm.`,
          local: `"${note.title}" will be moved to the .trash folder beside your notes. Click again to confirm.`,
        }[note.storage_target] || `Delete "${note.title}"? Click again to confirm.`
      );
      return;
    }

    disarm();
    performDelete(note, btn);
  });

  return btn;
}

function performDelete(note, btn) {
  btn.disabled = true;
  showNotice(`Deleting "${note.title}"…`);

  chrome.runtime.sendMessage(
    {
      type: "DELETE_NOTE",
      payload: {
        note_id: note.id,
        storage_target: note.storage_target,
        local_path: note.local_path,
        apple_note_id: note.apple_note_id,
      },
    },
    (response) => {
      const err = responseError(response);
      if (err) {
        btn.disabled = false;
        showError(`Couldn't delete "${note.title}":\n${err}`);
        return;
      }
      clearError();
      loadNotes();
    }
  );
}

function openNote(note) {
  if (note.storage_target === "drive" && note.drive_file_id) {
    chrome.tabs.create({
      url: `https://docs.google.com/document/d/${note.drive_file_id}/edit`,
    });
  } else if (note.storage_target === "apple_notes") {
    // The whole point of this target: one click lands you in Notes.app,
    // rather than hunting for a file and picking an editor.
    showNotice(`Opening "${note.title}" in Notes…`);
    chrome.runtime.sendMessage(
      { type: "OPEN_APPLE_NOTE", payload: { apple_note_id: note.apple_note_id } },
      (response) => {
        const err = responseError(response);
        if (err) {
          showError(`Couldn't open "${note.title}" in Notes:\n${err}`);
          return;
        }
        clearError();
        window.close();
      }
    );
  } else if (note.storage_target === "local") {
    // Ask the helper to open it in whatever handles Markdown. Copying the
    // path is the fallback, not the feature: it left the user to find the
    // file and pick an editor themselves.
    showNotice(`Opening "${note.title}"…`);
    chrome.runtime.sendMessage(
      { type: "OPEN_LOCAL_NOTE", payload: { local_path: note.local_path } },
      (response) => {
        const err = responseError(response);
        if (!err) {
          clearError();
          window.close();
          return;
        }
        // Most likely the helper isn't installed. Fall back to the path so
        // the note is still reachable by hand.
        const path = note.local_path || "(path unknown)";
        if (note.local_path && navigator.clipboard) {
          navigator.clipboard.writeText(note.local_path).then(
            () => showNotice(`Couldn't open it — path copied instead:\n${path}`),
            () => showNotice(`Couldn't open it. The note is at:\n${path}`)
          );
        } else {
          showNotice(`Couldn't open it. The note is at:\n${path}`);
        }
      }
    );
  }
}

// Creating a note is not instant — a Drive note waits on the Docs API, an
// Apple note on osascript. Saying so beats a popup that looks frozen.
const CREATING_LABEL = {
  drive: "Creating note in Google Drive…",
  apple_notes: "Creating note in Apple Notes…",
  local: "Creating local note…",
};

function createNote() {
  const title = window.prompt("New note title:");
  if (!title) return;

  const storageTarget = document.getElementById("storage-target").value;
  const button = document.getElementById("new-note-btn");

  // Disabled while in flight: a second click would create a duplicate.
  button.disabled = true;
  showNotice(CREATING_LABEL[storageTarget] || "Creating note…");

  chrome.runtime.sendMessage(
    { type: "CREATE_NOTE", payload: { title, storage_target: storageTarget } },
    (response) => {
      button.disabled = false;

      const err = responseError(response);
      if (err) {
        showError("Couldn't create note:\n" + err);
        return;
      }
      showNotice('Created "' + title + '".');
      // Let the confirmation be readable, then get out of the way.
      setTimeout(clearError, 2500);
      loadNotes();
    }
  );
}
