package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// API is the helper's actual functionality, independent of how it is
// reached. Both transports — native messaging (how Chrome talks to it) and
// the HTTP server (kept for development) — dispatch into this, so the two
// cannot drift apart as features are added.
type API struct {
	store *Store
	apple AppleNotesStore
}

func NewAPI(store *Store, apple AppleNotesStore) *API {
	return &API{store: store, apple: apple}
}

// Error codes the extension can act on. The transport maps these onto
// whatever it uses to signal failure — an HTTP status, or a field in the
// response envelope.
const (
	CodeInvalid     = "invalid"     // bad arguments; the caller's fault
	CodeUnsupported = "unsupported" // not available on this platform
	CodeForbidden   = "forbidden"   // macOS withheld automation permission
	CodeFailed      = "failed"      // everything else
)

type APIError struct {
	Code string
	Err  error
}

func (e *APIError) Error() string { return e.Err.Error() }
func (e *APIError) Unwrap() error { return e.Err }

func fail(code string, err error) error { return &APIError{Code: code, Err: err} }

// CodeOf classifies an error for the transports.
func CodeOf(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	switch {
	case errors.Is(err, ErrAppleNotesUnsupported):
		return CodeUnsupported
	case errors.Is(err, ErrAppleNotesNotAuthorized):
		return CodeForbidden
	}
	return CodeInvalid
}

// Method names. These are the wire contract with the extension; changing
// one means bumping Version and HELPER_MIN_VERSION together.
const (
	MethodHealth      = "health"
	MethodNoteCreate  = "note.create"
	MethodNoteAppend  = "note.append"
	MethodNoteDelete  = "note.delete"
	MethodNoteWrite   = "note.write"
	MethodNoteRead    = "note.read"
	MethodAppleCreate = "apple.create"
	MethodAppleAppend = "apple.append"
	MethodAppleDelete = "apple.delete"
	MethodAppleOpen   = "apple.open"
	MethodAppleWrite  = "apple.write"
	MethodAppleRead   = "apple.read"
)

// params covers every method's arguments. One flat struct rather than a
// type per method: the set is small, and it keeps decoding to a single
// step in both transports.
type params struct {
	Title       string `json:"title"`
	Path        string `json:"path"`
	ID          string `json:"id"`
	Text        string `json:"text"`
	SourceURL   string `json:"source_url"`
	SourceTitle string `json:"source_title"`
	Content     string `json:"content"`
	HTML        string `json:"html"`
}

// Dispatch runs one method and returns its result payload.
func (a *API) Dispatch(method string, raw json.RawMessage) (map[string]any, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fail(CodeInvalid, fmt.Errorf("invalid arguments: %w", err))
		}
	}

	switch method {
	case MethodHealth:
		appleStatus := "ok"
		if err := a.apple.Available(); err != nil {
			appleStatus = err.Error()
		}
		return map[string]any{
			"status":      "ok",
			"version":     Version,
			"dir":         a.store.Dir(),
			"apple_notes": appleStatus,
		}, nil

	case MethodNoteCreate:
		path, err := a.store.CreateNote(p.Title)
		if err != nil {
			return nil, fail(CodeInvalid, err)
		}
		return map[string]any{"path": path, "name": baseName(path)}, nil

	case MethodNoteAppend:
		if err := a.store.AppendCapture(p.Path, p.Text, p.SourceURL); err != nil {
			return nil, fail(CodeInvalid, err)
		}
		return map[string]any{"ok": true}, nil

	case MethodNoteDelete:
		trashed, err := a.store.DeleteNote(p.Path)
		if err != nil {
			return nil, fail(CodeInvalid, err)
		}
		return map[string]any{"trashed": trashed}, nil

	case MethodNoteWrite:
		backup, err := a.store.WriteNote(p.Path, p.Content)
		if err != nil {
			return nil, fail(CodeInvalid, err)
		}
		return map[string]any{"ok": true, "backup": backup}, nil

	case MethodNoteRead:
		content, err := a.store.ReadNote(p.Path)
		if err != nil {
			return nil, fail(CodeInvalid, err)
		}
		return map[string]any{"content": content}, nil

	case MethodAppleCreate:
		id, err := a.apple.CreateNote(p.Title)
		if err != nil {
			return nil, fail(CodeOf(err), err)
		}
		return map[string]any{"id": id}, nil

	case MethodAppleAppend:
		if err := a.apple.AppendCapture(p.ID, p.Text, p.SourceURL); err != nil {
			return nil, fail(CodeOf(err), err)
		}
		return map[string]any{"ok": true}, nil

	case MethodAppleDelete:
		if err := a.apple.DeleteNote(p.ID); err != nil {
			return nil, fail(CodeOf(err), err)
		}
		return map[string]any{"ok": true}, nil

	case MethodAppleOpen:
		if err := a.apple.ShowNote(p.ID); err != nil {
			return nil, fail(CodeOf(err), err)
		}
		return map[string]any{"ok": true}, nil

	case MethodAppleRead:
		html, err := a.apple.ReadBody(p.ID)
		if err != nil {
			return nil, fail(CodeOf(err), err)
		}
		return map[string]any{"html": html}, nil

	case MethodAppleWrite:
		if err := a.apple.ReplaceBody(p.ID, p.HTML); err != nil {
			return nil, fail(CodeOf(err), err)
		}
		return map[string]any{"ok": true}, nil
	}

	return nil, fail(CodeInvalid, fmt.Errorf("unknown method %q", method))
}

func baseName(path string) string { return filepath.Base(path) }
