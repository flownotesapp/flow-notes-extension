package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestCreateNoteWritesTitledFile(t *testing.T) {
	s := newTestStore(t)

	path, err := s.CreateNote("Reading list")
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if got := filepath.Base(path); got != "reading-list.md" {
		t.Errorf("filename = %q, want reading-list.md", got)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading note: %v", err)
	}
	if string(body) != "# Reading list\n" {
		t.Errorf("contents = %q", body)
	}
}

func TestCreateNoteDeduplicatesFilenames(t *testing.T) {
	s := newTestStore(t)

	first, _ := s.CreateNote("Reading list")
	second, err := s.CreateNote("Reading list")
	if err != nil {
		t.Fatalf("second CreateNote: %v", err)
	}
	if first == second {
		t.Fatal("second note reused the first note's path")
	}
	if got := filepath.Base(second); got != "reading-list-2.md" {
		t.Errorf("second filename = %q, want reading-list-2.md", got)
	}
}

func TestAppendCaptureWritesSourceLink(t *testing.T) {
	s := newTestStore(t)
	path, _ := s.CreateNote("Notes")

	if err := s.AppendCapture(path, "  A useful line.\r\n", "https://example.com/a"); err != nil {
		t.Fatalf("AppendCapture: %v", err)
	}
	body, _ := os.ReadFile(path)

	want := "# Notes\n\nA useful line.\n\n[source](https://example.com/a)\n"
	if string(body) != want {
		t.Errorf("contents = %q,\nwant %q", body, want)
	}
}

func TestAppendCaptureSkipsMarkerForUnusableURL(t *testing.T) {
	for _, raw := range []string{"", "javascript:alert(1)", "not a url", "ftp://example.com/x"} {
		t.Run(raw, func(t *testing.T) {
			s := newTestStore(t)
			path, _ := s.CreateNote("Notes")

			if err := s.AppendCapture(path, "text", raw); err != nil {
				t.Fatalf("AppendCapture: %v", err)
			}
			body, _ := os.ReadFile(path)
			if strings.Contains(string(body), "[source]") {
				t.Errorf("wrote a source link for an unusable url: %q", body)
			}
		})
	}
}

// The path in a capture request comes from the browser, so containment is
// the security boundary of this whole process.
func TestAppendCaptureRejectsPathsOutsideNotesDir(t *testing.T) {
	s := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"absolute path elsewhere": outside,
		"parent traversal":        filepath.Join(s.Dir(), "..", filepath.Base(outside)),
		"nested traversal":        filepath.Join(s.Dir(), "sub", "..", "..", "escape.md"),
		"non-markdown in dir":     filepath.Join(s.Dir(), "notes.txt"),
		"empty":                   "",
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.AppendCapture(path, "pwned", "https://example.com"); err == nil {
				t.Fatalf("expected rejection for %q", path)
			}
		})
	}

	if body, _ := os.ReadFile(outside); string(body) != "original\n" {
		t.Errorf("file outside the notes dir was modified: %q", body)
	}
}

// A symlink planted inside the notes directory must not redirect a write.
func TestAppendCaptureRejectsSymlinkedNote(t *testing.T) {
	s := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(s.Dir(), "innocent.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := s.AppendCapture(link, "pwned", "https://example.com"); err == nil {
		t.Fatal("expected the symlinked note to be rejected")
	}
	if body, _ := os.ReadFile(outside); string(body) != "original\n" {
		t.Errorf("symlink target was modified: %q", body)
	}
}

func TestAppendCaptureRejectsMissingNote(t *testing.T) {
	s := newTestStore(t)
	if err := s.AppendCapture(filepath.Join(s.Dir(), "nope.md"), "x", ""); err == nil {
		t.Fatal("expected an error for a note that does not exist")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Reading list":           "reading-list",
		"  Spaced  out  ":        "spaced-out",
		"Slashes/and:colons":     "slashes-and-colons",
		"../../etc/passwd":       "etc-passwd",
		"!!!":                    "note",
		"":                       "note",
		"Ünïcode Títles":         "ünïcode-títles",
		"日本語のノート":                "日本語のノート",
		strings.Repeat("a", 100): strings.Repeat("a", 60),
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeleteNoteMovesToTrashRatherThanUnlinking(t *testing.T) {
	s := newTestStore(t)
	path, _ := s.CreateNote("Doomed")
	if err := s.AppendCapture(path, "precious writing", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)

	trashed, err := s.DeleteNote(path)
	if err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Error("note is still in the notes directory")
	}
	if filepath.Dir(trashed) != filepath.Join(s.Dir(), trashDirName) {
		t.Errorf("trashed to %q, want a path inside %s", trashed, trashDirName)
	}
	recovered, err := os.ReadFile(trashed)
	if err != nil {
		t.Fatalf("reading trashed note: %v", err)
	}
	if string(recovered) != string(original) {
		t.Error("trashed copy does not match the original contents")
	}
}

// A note already gone from disk must still be deletable, or the extension
// is left with a list entry it can never remove.
func TestDeleteNoteIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	path, _ := s.CreateNote("Doomed")

	if _, err := s.DeleteNote(path); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	trashed, err := s.DeleteNote(path)
	if err != nil {
		t.Fatalf("second delete should succeed, got: %v", err)
	}
	if trashed != "" {
		t.Errorf("second delete reported trashing %q, want empty", trashed)
	}
}

// Delete takes a browser-supplied path, so it needs the same containment
// guarantee as append.
func TestDeleteNoteRejectsPathsOutsideNotesDir(t *testing.T) {
	s := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteNote(outside); err == nil {
		t.Fatal("expected rejection for a path outside the notes directory")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Error("file outside the notes directory was moved or removed")
	}
}

// Trashed notes live in a subdirectory, so they must fail the "directly
// inside the notes dir" check and be un-appendable.
func TestAppendCaptureRejectsTrashedNote(t *testing.T) {
	s := newTestStore(t)
	path, _ := s.CreateNote("Doomed")
	trashed, err := s.DeleteNote(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.AppendCapture(trashed, "text", "https://example.com"); err == nil {
		t.Error("expected appending to a trashed note to be rejected")
	}
}

// Organizing rewrites the whole note, so anything the user typed by hand
// must survive somewhere.
func TestWriteNoteBacksUpPreviousContents(t *testing.T) {
	s := newTestStore(t)
	path, _ := s.CreateNote("Notes")
	if err := s.AppendCapture(path, "hand-written thought", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	backup, err := s.WriteNote(path, "# Reorganised\n\nNew body.")
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	now, _ := os.ReadFile(path)
	if !strings.Contains(string(now), "Reorganised") {
		t.Errorf("note was not rewritten: %q", now)
	}
	if backup == "" {
		t.Fatal("no backup path returned")
	}
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(saved) != string(before) {
		t.Error("backup does not match the previous contents")
	}
	if filepath.Dir(backup) != filepath.Join(s.Dir(), trashDirName) {
		t.Errorf("backup went to %q, want it in %s", backup, trashDirName)
	}
}

func TestWriteNoteRejectsEmptyContentAndBadPaths(t *testing.T) {
	s := newTestStore(t)
	path, _ := s.CreateNote("Notes")

	if _, err := s.WriteNote(path, "   \n "); err == nil {
		t.Error("expected empty content to be refused")
	}
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteNote(outside, "pwned"); err == nil {
		t.Error("expected a path outside the notes directory to be refused")
	}
	if body, _ := os.ReadFile(outside); string(body) != "original\n" {
		t.Error("file outside the notes directory was overwritten")
	}
}
