package session

import (
	"errors"
	"fmt"

	libghostty "github.com/mitchellh/go-libghostty"
)

// ReplayFormat selects the snapshot output format from a Replay
// terminal.
type ReplayFormat int

const (
	// ReplayFormatVT emits VT-replayable bytes — feed back into a
	// fresh terminal and the screen reproduces.
	ReplayFormatVT ReplayFormat = iota

	// ReplayFormatPlain emits plain text (codepoints + newlines, no
	// SGR/cursor escapes). Greppable.
	ReplayFormatPlain
)

// Replay is a libghostty Terminal without a PTY master, suitable
// for off-line replay of a recorded byte stream into a snapshotable
// VT state machine. Used by `hoot log --format vt|plain` to render
// a recording's final visible state.
//
// Replay is single-goroutine: callers feed bytes via VTWrite and
// resize via Resize, then call Snapshot. No concurrent access; the
// underlying libghostty.Terminal is not safe for concurrent
// mutation.
type Replay struct {
	term *libghostty.Terminal
}

// NewLibghosttyReplay constructs a Replay with the given initial
// dimensions. cols and rows must match the recording's first
// (prelude) resize frame for faithful reproduction.
func NewLibghosttyReplay(cols, rows uint16) (*Replay, error) {
	if cols == 0 {
		cols = defaultRecorderCols
	}
	if rows == 0 {
		rows = defaultRecorderRows
	}
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollback(10_000),
	)
	if err != nil {
		return nil, fmt.Errorf("replay: new terminal: %w", err)
	}
	return &Replay{term: term}, nil
}

// VTWrite feeds payload bytes into the emulator. Same semantics as
// LibghosttyPTY's VTWrite, minus the PTY tee.
func (r *Replay) VTWrite(payload []byte) {
	if r == nil || r.term == nil {
		return
	}
	r.term.VTWrite(payload)
}

// Resize updates the emulator's grid. cellWidthPx/cellHeightPx are
// fixed at 0 — they only matter for kitty graphics, which the
// formatter intentionally doesn't replay.
func (r *Replay) Resize(cols, rows uint16) error {
	if r == nil || r.term == nil {
		return errors.New("replay: closed")
	}
	return r.term.Resize(cols, rows, 0, 0)
}

// Snapshot returns the recording's current visible state in the
// requested format. ReplayFormatVT emits VT-replayable bytes (what
// a real terminal would render); ReplayFormatPlain emits the
// codepoints + newlines.
//
// The formatter walks the active screen plus scrollback ring, so
// the snapshot covers the full recorded session — viewport plus
// history — up to max_scrollback rows.
func (r *Replay) Snapshot(format ReplayFormat) ([]byte, error) {
	if r == nil || r.term == nil {
		return nil, errors.New("replay: closed")
	}
	var fmtConst libghostty.FormatterFormat
	switch format {
	case ReplayFormatVT:
		fmtConst = libghostty.FormatterFormatVT
	case ReplayFormatPlain:
		fmtConst = libghostty.FormatterFormatPlain
	default:
		return nil, fmt.Errorf("replay: unknown format %d", format)
	}
	f, err := libghostty.NewFormatter(r.term,
		libghostty.WithFormatterFormat(fmtConst),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Format()
}

// Close releases the underlying libghostty Terminal. Safe to call
// multiple times.
func (r *Replay) Close() {
	if r == nil || r.term == nil {
		return
	}
	r.term.Close()
	r.term = nil
}
