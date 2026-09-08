package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeApple stands in for the Apple Notes store so the non-macOS and
// permission-denied paths can be exercised on any platform. The real
// unsupportedAppleNotes lives behind a !darwin build tag and so can't be
// compiled — let alone run — on the Mac this is developed on.
type fakeApple struct{ err error }

func (f fakeApple) Available() error                           { return f.err }
func (f fakeApple) CreateNote(string) (string, error)          { return "", f.err }
func (f fakeApple) AppendCapture(string, string, string) error { return f.err }
func (f fakeApple) DeleteNote(string) error                    { return f.err }
func (f fakeApple) ShowNote(string) error                      { return f.err }
func (f fakeApple) ReplaceBody(string, string) error           { return f.err }
func (f fakeApple) ReadBody(string) (string, error)            { return "", f.err }

func appleTestServer(t *testing.T, err error) http.Handler {
	t.Helper()
	store, storeErr := NewStore(t.TempDir())
	if storeErr != nil {
		t.Fatalf("NewStore: %v", storeErr)
	}
	return withCORS("", routes(NewAPI(store, fakeApple{err: err})))
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// What a Windows or Linux user gets if they pick the Apple Notes target.
func TestAppleNotesOnUnsupportedPlatform(t *testing.T) {
	h := appleTestServer(t, ErrAppleNotesUnsupported)

	for _, tc := range []struct{ path, body string }{
		{"/apple/notes", `{"title":"Test"}`},
		{"/apple/captures", `{"id":"x","text":"hi"}`},
		{"/apple/notes/delete", `{"id":"x"}`},
		{"/apple/notes/open", `{"id":"x"}`},
		{"/apple/notes/write", `{"id":"x","html":"<div>hi</div>"}`},
		{"/apple/notes/read", `{"id":"x"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := postJSON(t, h, tc.path, tc.body)

			if rec.Code != http.StatusNotImplemented {
				t.Errorf("status = %d, want 501", rec.Code)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("response was not JSON: %v", err)
			}
			// The message has to name the actual reason: this reaches the
			// user verbatim through the extension's error banner.
			if !strings.Contains(body.Error, "only available on macOS") {
				t.Errorf("error = %q, want it to explain the platform limit", body.Error)
			}
		})
	}
}

// The rest of the server keeps working on those platforms — only the
// Apple routes are unavailable.
func TestLocalMarkdownStillWorksWithoutAppleNotes(t *testing.T) {
	h := appleTestServer(t, ErrAppleNotesUnsupported)

	// The dev HTTP transport returns 200 for every success; status is no
	// longer the contract, since the native transport signals outcome with
	// the "ok" and "code" fields in the reply envelope.
	rec := postJSON(t, h, "/notes", `{"title":"Still fine"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("creating a Markdown note = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "still-fine.md") {
		t.Errorf("response did not name the created file: %s", rec.Body)
	}
}

func TestHealthzReportsAppleNotesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	appleTestServer(t, ErrAppleNotesUnsupported).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var body struct {
		Status     string `json:"status"`
		Version    string `json:"version"`
		AppleNotes string `json:"apple_notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz was not JSON: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok — the server itself is healthy", body.Status)
	}
	if !strings.Contains(body.AppleNotes, "only available on macOS") {
		t.Errorf("apple_notes = %q, want the platform reason", body.AppleNotes)
	}
	// The extension compares this against its own minimum to detect a
	// stale helper, so an empty value would silently disable that check.
	if body.Version != Version {
		t.Errorf("version = %q, want %q", body.Version, Version)
	}
}

// Denied automation permission is a different problem with a different
// fix, so it must not look like the platform being unsupported.
func TestAppleNotesPermissionDeniedIsDistinct(t *testing.T) {
	h := appleTestServer(t, ErrAppleNotesNotAuthorized)
	rec := postJSON(t, h, "/apple/notes", `{"title":"Test"}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "System Settings") {
		t.Errorf("error = %s, want it to name the fix", rec.Body)
	}
}
