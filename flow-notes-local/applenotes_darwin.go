package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// appleNotes drives Notes.app through osascript.
//
// Every script uses AppleScript's `on run argv` form and receives its data
// as process arguments. Nothing is ever interpolated into script source:
// capture text comes from arbitrary web pages, and building a script by
// concatenation would make that text executable.
type appleNotes struct {
	folder string // optional Notes folder to file new notes under
}

func NewAppleNotesStore(folder string) AppleNotesStore {
	return &appleNotes{folder: strings.TrimSpace(folder)}
}

// scriptTimeout is generous because the very first call can sit waiting on
// the macOS automation permission dialog.
const scriptTimeout = 60 * time.Second

func (a *appleNotes) Available() error {
	// `name of application` doesn't send an Apple event to Notes, so this
	// checks that the app exists without triggering the permission prompt.
	_, err := a.run(`on run argv
	return name of application "Notes"
end run`)
	return err
}

func (a *appleNotes) CreateNote(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	script := `on run argv
	set noteBody to item 1 of argv
	set folderName to item 2 of argv
	tell application "Notes"
		if folderName is "" then
			set newNote to make new note with properties {body:noteBody}
		else
			if not (exists folder folderName) then
				make new folder with properties {name:folderName}
			end if
			set newNote to make new note at folder folderName with properties {body:noteBody}
		end if
		return id of newNote
	end tell
end run`

	id, err := a.run(script, initialNoteBody(title), a.folder)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("Notes did not return an identifier for the new note")
	}
	return id, nil
}

func (a *appleNotes) AppendCapture(noteID, text, sourceURL string) error {
	if strings.TrimSpace(noteID) == "" {
		return fmt.Errorf("note id is required")
	}
	if normalizeCaptureText(text) == "" {
		return fmt.Errorf("capture text is empty")
	}

	// Read-modify-write: Notes exposes no append operation, only a
	// read-write `body` property.
	script := `on run argv
	set noteID to item 1 of argv
	set extra to item 2 of argv
	tell application "Notes"
		set theNote to note id noteID
		set body of theNote to ((body of theNote) & extra)
	end tell
	return "ok"
end run`

	_, err := a.run(script, noteID, captureHTML(text, sourceURL))
	return err
}

func (a *appleNotes) DeleteNote(noteID string) error {
	if strings.TrimSpace(noteID) == "" {
		return fmt.Errorf("note id is required")
	}

	// `delete` sends the note to Notes' Recently Deleted, where it stays
	// recoverable for 30 days — consistent with Drive trash and the local
	// .trash directory. Deleting a note that is already gone is treated as
	// success so the extension can always drop it from the list.
	script := `on run argv
	set noteID to item 1 of argv
	tell application "Notes"
		try
			delete note id noteID
		on error errMsg number errNum
			if errNum is -1728 then
				return "missing"
			end if
			error errMsg number errNum
		end try
	end tell
	return "ok"
end run`

	_, err := a.run(script, noteID)
	return err
}

// ReplaceBody overwrites a note's whole body, used by the organize pass.
//
// No backup is taken here, unlike the file store: Notes keeps its own
// version history, and the capture log in Postgres can regenerate the note
// regardless.
func (a *appleNotes) ReplaceBody(noteID, bodyHTML string) error {
	if strings.TrimSpace(noteID) == "" {
		return fmt.Errorf("note id is required")
	}
	if strings.TrimSpace(bodyHTML) == "" {
		return fmt.Errorf("refusing to write an empty body over a note")
	}

	script := `on run argv
	set noteID to item 1 of argv
	set newBody to item 2 of argv
	tell application "Notes"
		set body of (note id noteID) to newBody
	end tell
	return "ok"
end run`

	_, err := a.run(script, noteID, bodyHTML)
	return err
}

// ReadBody returns a note's body as HTML.
//
// Deliberately `body` and not `plaintext`: plaintext would drop the href
// from every [source] link, so a note organized twice would lose all its
// citations. The model is told the input may be HTML.
func (a *appleNotes) ReadBody(noteID string) (string, error) {
	if strings.TrimSpace(noteID) == "" {
		return "", fmt.Errorf("note id is required")
	}

	script := `on run argv
	set noteID to item 1 of argv
	tell application "Notes"
		return body of (note id noteID)
	end tell
end run`

	return a.run(script, noteID)
}

func (a *appleNotes) ShowNote(noteID string) error {
	if strings.TrimSpace(noteID) == "" {
		return fmt.Errorf("note id is required")
	}

	script := `on run argv
	set noteID to item 1 of argv
	tell application "Notes"
		activate
		show note id noteID
	end tell
	return "ok"
end run`

	_, err := a.run(script, noteID)
	return err
}

func (a *appleNotes) run(script string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()

	argv := append([]string{"-e", script}, args...)
	cmd := exec.CommandContext(ctx, "osascript", argv...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())

		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf(
				"Notes did not respond within %s — if macOS is showing a permission "+
					"dialog, approve it and try again", scriptTimeout)
		}
		// -1743 is errAEEventNotPermitted: the automation permission was
		// denied or never granted.
		if strings.Contains(message, "-1743") || strings.Contains(message, "Not authorized to send Apple events") {
			return "", ErrAppleNotesNotAuthorized
		}
		if message == "" {
			return "", fmt.Errorf("osascript failed: %w", err)
		}
		return "", fmt.Errorf("Notes error: %s", message)
	}
	return strings.TrimSpace(stdout.String()), nil
}
