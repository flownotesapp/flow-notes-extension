package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// trashDirName is where deleted notes go, inside the notes directory.
// Deleting a note should not be able to destroy writing the user cannot
// get back, so nothing here calls os.Remove on a note file.
const trashDirName = ".trash"

// errNoSuchNote lets callers distinguish "this note is already gone" —
// which makes a delete idempotent — from a rejected or unsafe path.
var errNoSuchNote = errors.New("no such note")

// Store owns a single directory of Markdown notes. Every path it hands out
// or accepts is validated to live directly inside that directory — the
// server takes file paths from the browser, so this is the boundary that
// keeps a crafted request from writing anywhere else on disk.
type Store struct {
	dir string // absolute, symlinks resolved
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("notes directory is required")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving notes directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("creating notes directory %s: %w", abs, err)
	}

	// Resolve symlinks once, up front, so the containment check below
	// compares like with like. On macOS /tmp is a symlink to /private/tmp,
	// which would otherwise make every path look out-of-bounds.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolving notes directory: %w", err)
	}

	return &Store{dir: resolved}, nil
}

func (s *Store) Dir() string { return s.dir }

// CreateNote writes a new Markdown file for title and returns its absolute
// path. Filenames are derived from the title and de-duplicated, so two
// notes called "Reading list" become reading-list.md and reading-list-2.md
// rather than one clobbering the other.
func (s *Store) CreateNote(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	name, err := s.uniqueName(slugify(title))
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, name)

	// O_EXCL: never silently take over a file that appeared between the
	// uniqueness check and here.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating note file: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "# %s\n", title); err != nil {
		return "", fmt.Errorf("writing note header: %w", err)
	}
	return path, nil
}

// AppendCapture appends a captured highlight to an existing note file,
// followed by a Markdown link back to the source page — the local
// equivalent of the "[source]" marker the Docs writer applies.
func (s *Store) AppendCapture(path, text, sourceURL string) error {
	safe, err := s.resolveExisting(path)
	if err != nil {
		return err
	}

	text = normalizeCaptureText(text)
	if text == "" {
		return fmt.Errorf("capture text is empty")
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n")
	if link := linkableURL(sourceURL); link != "" {
		// Escape the closing paren so a URL containing one can't break out
		// of the Markdown link.
		b.WriteString("\n[source](")
		b.WriteString(strings.ReplaceAll(link, ")", "%29"))
		b.WriteString(")\n")
	}

	f, err := os.OpenFile(safe, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening note file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("appending capture: %w", err)
	}
	return nil
}

// DeleteNote moves a note into the notes directory's .trash subdirectory
// rather than unlinking it. A misclick in the extension should cost the
// user a drag-and-drop to recover, not their notes.
//
// Deleting a note whose file is already gone succeeds: the caller's real
// goal is for the note to stop existing, and failing here would strand an
// un-removable entry in the note list.
func (s *Store) DeleteNote(path string) (string, error) {
	safe, err := s.resolveExisting(path)
	if err != nil {
		if errors.Is(err, errNoSuchNote) {
			return "", nil
		}
		return "", err
	}

	trashDir := filepath.Join(s.dir, trashDirName)
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return "", fmt.Errorf("creating trash directory: %w", err)
	}

	// Timestamp prefix so deleting two same-named notes doesn't clobber
	// the first one's copy.
	stamp := time.Now().UTC().Format("20060102-150405")
	dest := filepath.Join(trashDir, stamp+"-"+filepath.Base(safe))

	if err := os.Rename(safe, dest); err != nil {
		return "", fmt.Errorf("moving note to trash: %w", err)
	}
	return dest, nil
}

// ReadNote returns a note's contents.
//
// Needed because the server keeps no copy of anything captured: to
// organize a note, something has to read the only copy, and for a local
// file that is this process.
func (s *Store) ReadNote(path string) (string, error) {
	safe, err := s.resolveExisting(path)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(safe)
	if err != nil {
		return "", fmt.Errorf("reading note: %w", err)
	}
	return string(body), nil
}

// WriteNote replaces a note's contents, keeping a timestamped copy of the
// previous version in .trash first.
//
// Organizing rewrites the whole note from the capture log, so anything the
// user typed into the file by hand would otherwise be lost silently. The
// backup makes that recoverable for the same reason deletes are trashed
// rather than unlinked.
func (s *Store) WriteNote(path, content string) (backup string, err error) {
	safe, err := s.resolveExisting(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("refusing to write empty content over a note")
	}

	if previous, err := os.ReadFile(safe); err == nil && len(previous) > 0 {
		trashDir := filepath.Join(s.dir, trashDirName)
		if err := os.MkdirAll(trashDir, 0o700); err != nil {
			return "", fmt.Errorf("creating trash directory: %w", err)
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		backup = filepath.Join(trashDir, stamp+"-before-organize-"+filepath.Base(safe))
		if err := os.WriteFile(backup, previous, 0o600); err != nil {
			return "", fmt.Errorf("backing up previous contents: %w", err)
		}
	}

	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(safe, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("writing note: %w", err)
	}
	return backup, nil
}

// resolveExisting validates that path names an existing .md file sitting
// directly inside the store's directory, and returns the cleaned path.
//
// The check is done against the resolved parent directory rather than a
// string prefix, so neither "../" segments nor a symlink planted inside
// the notes directory can point the write somewhere else.
func (s *Store) resolveExisting(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.Ext(path) != ".md" {
		return "", fmt.Errorf("not a note file: %s", path)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	// Resolve the parent, not the file: EvalSymlinks fails on a path whose
	// final element doesn't exist, and we want a clear "no such note"
	// rather than a confusing resolution error.
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("%w: %s", errNoSuchNote, path)
	}
	if parent != s.dir {
		return "", fmt.Errorf("path is outside the notes directory: %s", path)
	}

	final := filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(final)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errNoSuchNote, path)
	}
	// Reject symlinks: a link inside the notes directory could otherwise
	// redirect an append onto an arbitrary file.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	return final, nil
}

func (s *Store) uniqueName(base string) (string, error) {
	for i := 1; i < 1000; i++ {
		name := base + ".md"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.md", base, i)
		}
		if _, err := os.Lstat(filepath.Join(s.dir, name)); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", fmt.Errorf("could not find an unused filename for %q", base)
}

// slugify turns a note title into a safe, readable filename stem.
func slugify(title string) string {
	var b strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if slug == "" {
		// Titles made entirely of punctuation or scripts we strip would
		// otherwise produce an empty filename.
		slug = "note"
	}
	return slug
}

func normalizeCaptureText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

// linkableURL returns raw if it is a usable http(s) link, otherwise "".
// Restricting the scheme keeps a page-supplied URL from becoming a
// javascript: link inside the user's notes.
func linkableURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return raw
}
