package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNewRestorer(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "", want: "hoot"},
		{name: "hoot", want: "hoot"},
		{name: "dtach", want: "dtach"},
		{name: "bogus", wantErr: true},
		{name: "Hoot", wantErr: true}, // case-sensitive
	}
	for _, tt := range tests {
		got, err := newRestorer(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("newRestorer(%q) = %v, want error", tt.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("newRestorer(%q) error: %v", tt.name, err)
			continue
		}
		if got.Name() != tt.want {
			t.Errorf("newRestorer(%q).Name() = %q, want %q", tt.name, got.Name(), tt.want)
		}
	}
}

// dtachRestorer ----------------------------------------------------------

func TestDtachRestorer_AttachClearsScreen(t *testing.T) {
	r := &dtachRestorer{}
	var buf bytes.Buffer
	if err := r.Attach(&buf); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if buf.String() != seqClearScreen {
		t.Fatalf("Attach wrote %q, want %q", buf.String(), seqClearScreen)
	}
}

func TestDtachRestorer_Cleanup(t *testing.T) {
	r := &dtachRestorer{}
	var buf bytes.Buffer
	if err := r.Cleanup(&buf); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if buf.String() != seqCursorShow {
		t.Fatalf("Cleanup wrote %q, want %q", buf.String(), seqCursorShow)
	}
}

func TestDtachRestorer_ObserveIsNoOp(t *testing.T) {
	r := &dtachRestorer{}
	r.Observe([]byte("\x1b[>1u\x1b[>4;2m\x1b]9;4;1;50\x07anything"))
	var buf bytes.Buffer
	if err := r.Cleanup(&buf); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if buf.String() != seqCursorShow {
		t.Fatalf("Cleanup after Observe wrote %q, want %q (Observe should be a no-op)", buf.String(), seqCursorShow)
	}
}

// hootRestorer ----------------------------------------------------------

func TestHootRestorer_AttachPushesKittyFrame(t *testing.T) {
	r := &hootRestorer{}
	var buf bytes.Buffer
	if err := r.Attach(&buf); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if buf.String() != seqKittyKbdPushEmpty {
		t.Fatalf("Attach wrote %q, want %q", buf.String(), seqKittyKbdPushEmpty)
	}
	if got := r.kittyPops.Load(); got != 1 {
		t.Fatalf("kittyPops after Attach = %d, want 1", got)
	}
}

// errWriter is an io.Writer that fails on every Write. Used to verify that
// Attach doesn't mutate kittyPops if the push write fails.
type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("write failed") }

func TestHootRestorer_AttachWriteErrorPreservesState(t *testing.T) {
	r := &hootRestorer{}
	if err := r.Attach(errWriter{}); err == nil {
		t.Fatalf("Attach with errWriter returned nil error")
	}
	if got := r.kittyPops.Load(); got != 0 {
		t.Fatalf("kittyPops after failed Attach = %d, want 0 (no push happened, no pop owed)", got)
	}
}

func TestHootRestorer_CleanupAfterAttach(t *testing.T) {
	r := &hootRestorer{}
	if err := r.Attach(io.Discard); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	var buf bytes.Buffer
	if err := r.Cleanup(&buf); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Expect: kitty pop (from Attach push) + the always-emit catalogue.
	want := seqKittyKbdPop + strings.Join(hootCleanupSequence, "")
	if buf.String() != want {
		t.Fatalf("Cleanup mismatch.\n got %q\nwant %q", buf.String(), want)
	}
}

func TestHootRestorer_CleanupClearsOSCProgress(t *testing.T) {
	// Explicit regression test for the Ghostty progress-bar bug. Named for
	// the bug so a future contributor grepping for "progress" finds it.
	r := &hootRestorer{}
	var buf bytes.Buffer
	if err := r.Cleanup(&buf); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !strings.Contains(buf.String(), seqOSCProgressClear) {
		t.Fatalf("Cleanup output missing OSC 9;4;0 progress clear: %q", buf.String())
	}
}

// catalogueEntry mirrors a "decision = emit" row in spec.md's catalogue.
// Adding a row to the spec catalogue requires adding an entry here, otherwise
// TestHootRestorer_CleanupContainsAllCatalogueEntries fails. Removing a row
// requires removing the entry. The test is the coverage guard.
type catalogueEntry struct {
	name string // human-readable; matches the name in spec.md
	seq  string // the seqXxx constant
}

var catalogue = []catalogueEntry{
	// GUI affordance.
	{"osc-progress-clear", seqOSCProgressClear},

	// Mouse modes (8 entries).
	{"mouse-x10-off", seqMouseX10Off},
	{"mouse-normal-off", seqMouseNormalOff},
	{"mouse-button-off", seqMouseButtonOff},
	{"mouse-any-off", seqMouseAnyOff},
	{"mouse-fmt-utf8-off", seqMouseFmtUTF8Off},
	{"mouse-fmt-sgr-off", seqMouseFmtSGROff},
	{"mouse-fmt-urxvt-off", seqMouseFmtUrxvtOff},
	{"mouse-fmt-sgr-pixels-off", seqMouseFmtSGRPixelsOff},

	// Keyboard input modes (10 entries).
	{"app-cursor-keys-off", seqAppCursorKeysOff},
	{"app-keypad-dec-off", seqAppKeypadDECOff},
	{"app-keypad-esc-off", seqAppKeypadESCOff},
	{"backarrow-off", seqBackarrowOff},
	{"focus-events-off", seqFocusEventsOff},
	{"bracketed-paste-off", seqBracketedPasteOff},
	{"alt-sends-escape-off", seqAltSendsEscapeOff},
	{"disable-keyboard-off", seqDisableKeyboardOff},
	{"insert-mode-off", seqInsertModeOff},
	{"linefeed-mode-off", seqLinefeedModeOff},

	// Display modes (11 entries).
	{"reverse-colors-off", seqReverseColorsOff},
	{"origin-mode-off", seqOriginModeOff},
	{"wraparound-on", seqWraparoundOn},
	{"slow-scroll-off", seqSlowScrollOff},
	{"reverse-wrap-off", seqReverseWrapOff},
	{"reverse-wrap-ext-off", seqReverseWrapExtOff},
	{"132-column-off", seq132ColumnOff},
	{"sync-output-off", seqSyncOutputOff},
	{"grapheme-cluster-off", seqGraphemeClusterOff},
	{"report-color-scheme-off", seqReportColorSchemeOff},
	{"in-band-size-reports-off", seqInBandSizeReportsOff},

	// Layout (3 entries).
	{"left-right-margin-off", seqLeftRightMarginOff},
	{"scroll-region-reset", seqScrollRegionReset},
	{"cursor-shape-default", seqCursorShapeDefault},

	// Charset (1 entry).
	{"charset-g0-ascii", seqCharsetG0Ascii},

	// Alt screens (3 entries).
	{"alt-screen-47-off", seqAltScreen47Off},
	{"alt-screen-1047-off", seqAltScreen1047Off},
	{"alt-screen-1049-off", seqAltScreen1049Off},

	// Visual reset (3 entries).
	{"sgr-reset", seqSGRReset},
	{"cursor-show", seqCursorShow},
	{"clear-screen", seqClearScreen},
}

func TestHootRestorer_CleanupContainsAllCatalogueEntries(t *testing.T) {
	r := &hootRestorer{}
	var buf bytes.Buffer
	if err := r.Cleanup(&buf); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	got := buf.String()
	for _, e := range catalogue {
		if !strings.Contains(got, e.seq) {
			t.Errorf("Cleanup output missing %q (%q)", e.name, e.seq)
		}
	}
	// Also check that every emit in hootCleanupSequence is in the catalogue,
	// so we catch the inverse mistake (production list grew, test list didn't).
	if len(hootCleanupSequence) != len(catalogue) {
		t.Fatalf("hootCleanupSequence has %d entries; catalogue has %d. Add or remove a catalogueEntry to match.",
			len(hootCleanupSequence), len(catalogue))
	}
}

func TestHootRestorer_ObservesKittyPushPop(t *testing.T) {
	r := &hootRestorer{}
	r.Observe([]byte("\x1b[>1u"))
	if got := r.kittyPops.Load(); got != 1 {
		t.Fatalf("kittyPops after live push = %d, want 1", got)
	}
	r.Observe([]byte("\x1b[<u"))
	if got := r.kittyPops.Load(); got != 0 {
		t.Fatalf("kittyPops after matching pop = %d, want 0", got)
	}
}

func TestHootRestorer_ObservesModifyOtherKeys(t *testing.T) {
	r := &hootRestorer{}
	r.Observe([]byte("\x1b[>4;2m"))
	if !r.modifyOtherKeys.Load() {
		t.Fatalf("modifyOtherKeys after CSI > 4;2 m = false, want true")
	}
	r.Observe([]byte("\x1b[>4;0m"))
	if r.modifyOtherKeys.Load() {
		t.Fatalf("modifyOtherKeys after CSI > 4;0 m = true, want false")
	}
}

func TestHootRestorer_ObservesSplitCSI(t *testing.T) {
	r := &hootRestorer{}
	r.Observe([]byte("\x1b[>"))
	r.Observe([]byte("1u"))
	if got := r.kittyPops.Load(); got != 1 {
		t.Fatalf("kittyPops after split CSI > 1 u = %d, want 1", got)
	}
}

// emitDetach -------------------------------------------------------------

func TestEmitDetachUsesCleanupBeforeBanner(t *testing.T) {
	r := &hootRestorer{}
	if err := r.Attach(io.Discard); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	var buf bytes.Buffer
	emitDetach(&buf, attachLabel{Session: "s", Host: "h"}, r)

	var wantCleanup bytes.Buffer
	if err := r.Cleanup(&wantCleanup); err != nil {
		t.Fatalf("Cleanup (oracle): %v", err)
	}
	want := wantCleanup.String() + "\r\n[disconnected. s @ h]\r\n"
	if buf.String() != want {
		t.Fatalf("emitDetach mismatch.\n got %q\nwant %q", buf.String(), want)
	}
}

func TestEmitDetachWithNilRestorer(t *testing.T) {
	var buf bytes.Buffer
	emitDetach(&buf, attachLabel{Session: "s", Host: "h"}, nil)
	want := "\r\n[disconnected. s @ h]\r\n"
	if buf.String() != want {
		t.Fatalf("emitDetach(nil) = %q, want %q", buf.String(), want)
	}
}
