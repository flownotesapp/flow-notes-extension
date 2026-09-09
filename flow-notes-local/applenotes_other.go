//go:build !darwin

package main

// unsupportedAppleNotes stands in on platforms without Notes.app, so the
// local server still builds and runs everywhere — the Apple Notes routes
// simply report that the target is unavailable.
type unsupportedAppleNotes struct{}

func NewAppleNotesStore(string) AppleNotesStore { return unsupportedAppleNotes{} }

func (unsupportedAppleNotes) Available() error { return ErrAppleNotesUnsupported }

func (unsupportedAppleNotes) CreateNote(string) (string, error) {
	return "", ErrAppleNotesUnsupported
}

func (unsupportedAppleNotes) AppendCapture(string, string, string) error {
	return ErrAppleNotesUnsupported
}

func (unsupportedAppleNotes) DeleteNote(string) error { return ErrAppleNotesUnsupported }

func (unsupportedAppleNotes) ShowNote(string) error { return ErrAppleNotesUnsupported }

func (unsupportedAppleNotes) ReplaceBody(string, string) error {
	return ErrAppleNotesUnsupported
}

func (unsupportedAppleNotes) ReadBody(string) (string, error) {
	return "", ErrAppleNotesUnsupported
}
