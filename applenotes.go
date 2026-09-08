package main

import (
	"errors"
	"html"
	"strings"
)

// ErrAppleNotesUnsupported is returned on platforms without Notes.app.
var ErrAppleNotesUnsupported = errors.New("Apple Notes is only available on macOS")

// ErrAppleNotesNotAuthorized means macOS refused to let this process
// control Notes.app. Distinguished from other failures because the fix is
// a specific one the user has to perform in System Settings.
// macOS attributes an automation request to whatever launched the process,
// so the entry to enable is Chrome's when the helper runs as a native
// messaging host, and the terminal's when it is run by hand with -serve.
// Naming only one of them sends people looking in the wrong place.
var ErrAppleNotesNotAuthorized = errors.New(
	"macOS denied permission to control Notes. Open System Settings > " +
		"Privacy & Security > Automation and allow Notes under Google Chrome " +
		"(or under your terminal, if you started this helper yourself), then " +
		"try again")

// AppleNotesStore creates and edits notes in the macOS Notes app.
//
// Notes.app stores note bodies as HTML, which is a better fit for this
// project than Markdown: the "[source]" marker can be a real anchor, so a
// local note behaves exactly like the Google Docs version — a small
// clickable tag rather than a raw URL sitting in the text.
type AppleNotesStore interface {
	// Available reports whether Notes can be scripted at all.
	Available() error
	// CreateNote makes a note and returns its persistent identifier.
	CreateNote(title string) (string, error)
	// AppendCapture appends a highlight and its source link.
	AppendCapture(noteID, text, sourceURL string) error
	// DeleteNote moves a note to Notes' Recently Deleted.
	DeleteNote(noteID string) error
	// ShowNote brings the note to the front in Notes.app.
	ShowNote(noteID string) error
	// ReplaceBody overwrites a note's entire body with HTML.
	ReplaceBody(noteID, bodyHTML string) error
	// ReadBody returns a note's body as HTML.
	ReadBody(noteID string) (string, error)
}

// initialNoteBody is a new note's starting content. Notes derives the
// title shown in its list from the first line of the body, so the title
// has to be in the body — setting only the `name` property isn't enough.
func initialNoteBody(title string) string {
	return "<div><h1>" + html.EscapeString(title) + "</h1></div>"
}

// captureHTML renders one capture as Notes-compatible HTML.
//
// Every piece of page-derived text is HTML-escaped. Captures come from
// arbitrary web pages, so unescaped text would let a page inject markup —
// or an anchor of its own — into the user's notes.
func captureHTML(text, sourceURL string) string {
	var b strings.Builder

	// Blank line between captures, matching the Docs and Markdown writers.
	b.WriteString("<div><br></div>")

	for _, line := range strings.Split(normalizeCaptureText(text), "\n") {
		b.WriteString("<div>")
		if strings.TrimSpace(line) == "" {
			b.WriteString("<br>")
		} else {
			b.WriteString(html.EscapeString(line))
		}
		b.WriteString("</div>")
	}

	if link := linkableURL(sourceURL); link != "" {
		b.WriteString(`<div><a href="`)
		b.WriteString(html.EscapeString(link))
		b.WriteString(`">[source]</a></div>`)
	}
	return b.String()
}
