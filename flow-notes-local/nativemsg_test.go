package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAPI(t *testing.T, appleErr error) *API {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return NewAPI(store, fakeApple{err: appleErr}, "")
}

// encode frames a message the way Chrome does.
func encode(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var header [4]byte
	binary.NativeEndian.PutUint32(header[:], uint32(len(body)))
	buf.Write(header[:])
	buf.Write(body)
	return buf.Bytes()
}

// decodeAll reads every framed reply out of the output stream. Doing it by
// frame rather than by JSON boundary is the point — a framing bug is
// invisible to a JSON-only check but hangs the real channel.
func decodeAll(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	r := bytes.NewReader(raw)

	for {
		var header [4]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return out
		}
		body := make([]byte, binary.NativeEndian.Uint32(header[:]))
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatalf("reply body was shorter than its declared length: %v", err)
		}
		var msg map[string]any
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("reply was not valid JSON: %v (%q)", err, body)
		}
		out = append(out, msg)
	}
}

func run(t *testing.T, api *API, messages ...any) []map[string]any {
	t.Helper()
	var in bytes.Buffer
	for _, m := range messages {
		in.Write(encode(t, m))
	}
	var out bytes.Buffer
	if err := ServeNativeMessaging(api, &in, &out); err != nil {
		t.Fatalf("ServeNativeMessaging: %v", err)
	}
	return decodeAll(t, out.Bytes())
}

func TestNativeMessagingRoundTrip(t *testing.T) {
	api := testAPI(t, nil)

	replies := run(t, api,
		map[string]any{"id": 1, "method": MethodHealth},
		map[string]any{"id": 2, "method": MethodNoteCreate, "params": map[string]any{"title": "My note"}},
	)
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2", len(replies))
	}

	// Ids must be echoed, or the extension cannot match a reply to a call.
	if replies[0]["id"] != float64(1) || replies[1]["id"] != float64(2) {
		t.Errorf("ids not echoed: %v, %v", replies[0]["id"], replies[1]["id"])
	}
	if replies[0]["ok"] != true || replies[0]["version"] != Version {
		t.Errorf("health reply = %v", replies[0])
	}
	path, _ := replies[1]["path"].(string)
	if filepath.Base(path) != "my-note.md" {
		t.Errorf("created path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file was not actually created: %v", err)
	}
}

// Stdin closing is Chrome saying it is done — a normal exit, not a fault.
func TestNativeMessagingStopsCleanlyOnEOF(t *testing.T) {
	if err := ServeNativeMessaging(testAPI(t, nil), bytes.NewReader(nil), io.Discard); err != nil {
		t.Errorf("clean EOF returned %v, want nil", err)
	}
}

// A failure must come back as a reply the extension can read, not kill the
// channel — otherwise one bad call takes down every later one.
func TestNativeMessagingReportsErrorsInBand(t *testing.T) {
	replies := run(t, testAPI(t, ErrAppleNotesUnsupported),
		map[string]any{"id": 7, "method": MethodAppleCreate, "params": map[string]any{"title": "x"}},
		map[string]any{"id": 8, "method": MethodHealth},
	)
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2 — an error ended the loop", len(replies))
	}
	if replies[0]["ok"] != false || replies[0]["code"] != CodeUnsupported {
		t.Errorf("error reply = %v, want ok:false code:%s", replies[0], CodeUnsupported)
	}
	if !strings.Contains(replies[0]["error"].(string), "only available on macOS") {
		t.Errorf("error text = %v", replies[0]["error"])
	}
	if replies[1]["ok"] != true {
		t.Error("the call after a failure did not succeed")
	}
}

func TestNativeMessagingRejectsUnknownMethod(t *testing.T) {
	replies := run(t, testAPI(t, nil), map[string]any{"id": 1, "method": "note.explode"})
	if replies[0]["ok"] != false || !strings.Contains(replies[0]["error"].(string), "unknown method") {
		t.Errorf("reply = %v", replies[0])
	}
}

// Malformed framing must fail loudly rather than desynchronise the stream.
func TestNativeMessagingRejectsBadFraming(t *testing.T) {
	cases := map[string][]byte{
		"truncated header": {0x01, 0x02},
		"zero length":      {0, 0, 0, 0},
		"body shorter than declared header": func() []byte {
			var h [4]byte
			binary.NativeEndian.PutUint32(h[:], 100)
			return append(h[:], []byte(`{"id":1}`)...)
		}(),
		"oversized length": func() []byte {
			var h [4]byte
			binary.NativeEndian.PutUint32(h[:], maxIncomingMessage+1)
			return h[:]
		}(),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			err := ServeNativeMessaging(testAPI(t, nil), bytes.NewReader(raw), io.Discard)
			if err == nil {
				t.Error("expected an error for malformed framing")
			}
		})
	}
}

func TestNativeMessagingRejectsNonJSONBody(t *testing.T) {
	var h [4]byte
	body := []byte("not json at all")
	binary.NativeEndian.PutUint32(h[:], uint32(len(body)))

	err := ServeNativeMessaging(testAPI(t, nil), bytes.NewReader(append(h[:], body...)), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("error = %v, want it to name the JSON failure", err)
	}
}

// The full local-file flow over the wire, since this is what the extension
// actually does end to end.
func TestNativeMessagingFullNoteLifecycle(t *testing.T) {
	api := testAPI(t, nil)

	created := run(t, api, map[string]any{"id": 1, "method": MethodNoteCreate,
		"params": map[string]any{"title": "Lifecycle"}})[0]
	path := created["path"].(string)

	replies := run(t, api,
		map[string]any{"id": 2, "method": MethodNoteAppend, "params": map[string]any{
			"path": path, "text": "a highlight", "source_url": "https://example.com"}},
		map[string]any{"id": 3, "method": MethodNoteWrite, "params": map[string]any{
			"path": path, "content": "# Organised\n\nRewritten."}},
		map[string]any{"id": 4, "method": MethodNoteDelete, "params": map[string]any{"path": path}},
	)
	for i, r := range replies {
		if r["ok"] != true {
			t.Fatalf("step %d failed: %v", i+2, r)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("note still present after delete")
	}
	// The rewrite backed up the previous contents before overwriting.
	if backup, _ := replies[1]["backup"].(string); backup == "" {
		t.Error("write reported no backup of the previous contents")
	}
}
