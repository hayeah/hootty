package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
)

// TerminalRestorer manages local-terminal cleanup for one hoot attach
// session. Implementations are stateful and live for the duration of
// runAttachLoop; lifecycle methods Attach/Observe/Cleanup span reconnects.
//
// Lifecycle (enforced by runServerLoop, not by the restorer):
//   - Attach is called exactly once per process, on the false→true
//     transition of the runAttachLoop-scoped `attached` atomic when the
//     first MsgSnapshotScrollback or MsgSnapshotScreen frame arrives.
//   - Observe is called for every chunk of remote PTY output written to
//     the local terminal, including snapshot payloads.
//   - Cleanup is called exactly once at outer exit, from emitDetach.
//
// Across reconnects, Attach is NOT re-called: hoot's "stay raw, repaint
// snapshot, transparent retry" model means each drop+reconnect is invisible
// to the user. The reconnect snapshot itself re-establishes inner-program
// state via libghostty's serialized mode-set bytes.
type TerminalRestorer interface {
	Name() string
	Attach(w io.Writer) error
	Observe(payload []byte)
	Cleanup(w io.Writer) error
}

// newRestorer constructs the restorer named by name. "" and "hoot" both
// return the default hootRestorer (behavior-preserving).
func newRestorer(name string) (TerminalRestorer, error) {
	switch name {
	case "dtach":
		return &dtachRestorer{}, nil
	case "", "hoot":
		return &hootRestorer{}, nil
	default:
		return nil, fmt.Errorf("unknown --restorer: %q (want hoot or dtach)", name)
	}
}

// dtachRestorer tracks crigler/dtach's actual attach/detach behavior.
// Useful as an experimental control: with this restorer, escape state
// set by the inner program (kitty keyboard, alt-screen, mouse modes,
// OSC progress, etc.) leaks across detach exactly the way it would
// under dtach. Comparing --restorer=dtach against --restorer=hoot in
// real use tells us which pieces of hootRestorer's enumerated cleanup
// are load-bearing.
//
// dtach upstream (github.com/crigler/dtach):
//
//   - main.c:279       tcgetattr(0, &orig_term)
//   - attach.c:208     switch local terminal to raw mode
//   - attach.c:220     clear screen on attach: ESC [ H ESC [ 2 J
//   - attach.c:196     atexit(restore_term)
//   - attach.c:38      restore_term: tcsetattr orig_term + write \x1b[?25h
//
// Termios save/raw/restore is already handled by runAttachLoop
// (term.MakeRaw / term.Restore around the whole loop), matching dtach
// upstream. This restorer covers the display-side bytes only.
type dtachRestorer struct{}

func (*dtachRestorer) Name() string { return "dtach" }

func (*dtachRestorer) Attach(w io.Writer) error {
	// dtach/attach.c:220
	_, err := io.WriteString(w, seqClearScreen)
	return err
}

func (*dtachRestorer) Observe(_ []byte) {}

func (*dtachRestorer) Cleanup(w io.Writer) error {
	// dtach/attach.c:38 — restore_term emits this on exit.
	_, err := io.WriteString(w, seqCursorShow)
	return err
}

// hootRestorer is the default restorer.
//
// On Attach it claims a kitty kbd stack frame so the libghostty snapshot's
// kitty kbd SET bytes (CSI = ... u) land in a Hoot-owned frame rather than
// the user's pre-attach terminal frame. While attached it observes live CSI
// bytes for additional kitty pushes/pops and for modifyOtherKeys changes.
//
// On Cleanup it emits the comprehensive enumerated reset listed in spec.md's
// catalogue, plus the conditional state-derived prefix (kitty pop count,
// modifyOtherKeys off if observed) and the OSC 9;4;0 progress clear that
// fixes the Ghostty stuck-progress-bar bug after `hoot detach` while Claude
// Code's /usage was open.
type hootRestorer struct {
	kittyPops       atomic.Int32
	modifyOtherKeys atomic.Bool

	// Streaming-CSI parser scratch; only touched from Observe (which runs
	// single-threaded on runServerLoop's goroutine).
	csiBuf []byte
}

func (*hootRestorer) Name() string { return "hoot" }

func (r *hootRestorer) Attach(w io.Writer) error {
	// Push an empty Hoot-owned kitty kbd frame. Symmetric pop in Cleanup.
	// Sent unconditionally; on terminals without kitty kbd support the
	// unrecognized private-CSI parses-and-drops with no visible effect.
	//
	// If the write errors, kittyPops stays at 0 so Cleanup won't emit a
	// pop for a push that didn't happen.
	if _, err := io.WriteString(w, seqKittyKbdPushEmpty); err != nil {
		return err
	}
	r.kittyPops.Add(1)
	return nil
}

func (r *hootRestorer) Observe(payload []byte) {
	for _, b := range payload {
		if len(r.csiBuf) == 0 {
			if b == '\x1b' {
				r.csiBuf = append(r.csiBuf, b)
			}
			continue
		}
		if len(r.csiBuf) == 1 {
			if b == '[' {
				r.csiBuf = append(r.csiBuf, b)
				continue
			}
			if b == '\x1b' {
				r.csiBuf = r.csiBuf[:1]
				continue
			}
			r.csiBuf = r.csiBuf[:0]
			continue
		}

		r.csiBuf = append(r.csiBuf, b)
		if len(r.csiBuf) > 32 {
			r.csiBuf = r.csiBuf[:0]
			continue
		}
		if b >= 0x40 && b <= 0x7e {
			r.observeCSI(r.csiBuf[2:])
			r.csiBuf = r.csiBuf[:0]
		}
	}
}

func (r *hootRestorer) observeCSI(seq []byte) {
	if len(seq) < 2 {
		return
	}
	private := seq[0]
	final := seq[len(seq)-1]
	params := seq[1 : len(seq)-1]

	switch {
	case private == '>' && final == 'u':
		r.kittyPops.Add(1)
	case private == '<' && final == 'u':
		r.removeKittyKbdPop(parseCSIParamDefault(params, 1))
	case private == '>' && final == 'm':
		r.observeModifyOtherKeys(params)
	}
}

func (r *hootRestorer) observeModifyOtherKeys(params []byte) {
	if len(params) == 0 {
		r.modifyOtherKeys.Store(false)
		return
	}
	parts := strings.Split(string(params), ";")
	if parts[0] != "4" {
		return
	}
	r.modifyOtherKeys.Store(len(parts) >= 2 && parts[1] == "2")
}

func (r *hootRestorer) removeKittyKbdPop(n int) {
	if n <= 0 {
		return
	}
	for {
		current := r.kittyPops.Load()
		next := current - int32(n)
		if next < 0 {
			next = 0
		}
		if r.kittyPops.CompareAndSwap(current, next) {
			return
		}
	}
}

func (r *hootRestorer) Cleanup(w io.Writer) error {
	write := func(s string) error {
		_, err := io.WriteString(w, s)
		return err
	}

	// 1. Conditional state-derived prefix.
	if n := r.kittyPops.Load(); n > 0 {
		var s string
		if n == 1 {
			s = seqKittyKbdPop
		} else {
			s = fmt.Sprintf("\x1b[<%du", n)
		}
		if err := write(s); err != nil {
			return err
		}
	}
	if r.modifyOtherKeys.Load() {
		if err := write(seqModifyOtherKeysOff); err != nil {
			return err
		}
	}

	// 2-9. Always-emit. Each is an independently safe DECRST or private-CSI;
	// terminals that don't implement the mode parse-and-drop without effect.
	for _, s := range hootCleanupSequence {
		if err := write(s); err != nil {
			return err
		}
	}
	return nil
}

// hootCleanupSequence is the catalogue-ordered list of always-emit cleanup
// sequences for hootRestorer. Each entry corresponds to a row in spec.md's
// catalogue with decision = "emit". Adding a new entry is an edit here +
// a new constant below.
//
// TestHootRestorer_CleanupContainsAllCatalogueEntries asserts that every
// "emit" decision in the spec catalogue has a corresponding entry here.
var hootCleanupSequence = []string{
	// 2. GUI affordance clear (the codex Ghostty progress-bar fix).
	seqOSCProgressClear,

	// 3. Mouse modes — every mouse mode Ghostty knows about.
	seqMouseX10Off,          // ?9
	seqMouseNormalOff,       // ?1000
	seqMouseButtonOff,       // ?1002
	seqMouseAnyOff,          // ?1003
	seqMouseFmtUTF8Off,      // ?1005
	seqMouseFmtSGROff,       // ?1006
	seqMouseFmtUrxvtOff,     // ?1015
	seqMouseFmtSGRPixelsOff, // ?1016

	// 4. Keyboard input modes.
	seqAppCursorKeysOff,   // ?1
	seqAppKeypadDECOff,    // ?66
	seqAppKeypadESCOff,    // ESC > (DECKPNM)
	seqBackarrowOff,       // ?67
	seqFocusEventsOff,     // ?1004
	seqBracketedPasteOff,  // ?2004
	seqAltSendsEscapeOff,  // ?1039
	seqDisableKeyboardOff, // CSI 2 l (KAM)
	seqInsertModeOff,      // CSI 4 l (IRM)
	seqLinefeedModeOff,    // CSI 20 l (LNM)

	// 5. Display modes.
	seqReverseColorsOff,     // ?5
	seqOriginModeOff,        // ?6
	seqWraparoundOn,         // ?7 h (default-on; restore)
	seqSlowScrollOff,        // ?4
	seqReverseWrapOff,       // ?45
	seqReverseWrapExtOff,    // ?1045
	seq132ColumnOff,         // ?3
	seqSyncOutputOff,        // ?2026
	seqGraphemeClusterOff,   // ?2027
	seqReportColorSchemeOff, // ?2031
	seqInBandSizeReportsOff, // ?2048

	// 6. Layout / margins / scrolling.
	seqLeftRightMarginOff, // ?69
	seqScrollRegionReset,  // CSI r (DECSTBM, no params)
	seqCursorShapeDefault, // CSI 0 SP q (DECSCUSR)

	// 7. Charset.
	seqCharsetG0Ascii, // SI + ESC ( B

	// 8. Alt screens (in DECSET-number order so terminals that only
	// implement one of these get the matching reset).
	seqAltScreen47Off,   // ?47
	seqAltScreen1047Off, // ?1047
	seqAltScreen1049Off, // ?1049

	// 9. Visual reset.
	seqSGRReset,    // CSI 0 m
	seqCursorShow,  // ?25 h
	seqClearScreen, // CSI H CSI 2 J
}

// Per-mode terminal-control sequences. Each one is independently safe to
// send: DECRST forms (`CSI ? <n> l`) and the keyboard-protocol forms use
// private-use CSI prefixes, so terminals that don't implement the mode
// parse-and-drop the bytes without printing or responding.
//
// Constants are organized by catalogue category to mirror the spec.md
// catalogue table; see spec.md for the per-mode rationale and source-line
// references into Ghostty's modes.zig.
const (
	// Conditional / observer-driven state.
	seqKittyKbdPop        = "\x1b[<u"     // pop one kitty kbd stack level
	seqKittyKbdPushEmpty  = "\x1b[>u"     // push a Hoot-owned kitty kbd frame
	seqModifyOtherKeysOff = "\x1b[>4;0m"  // disable xterm modifyOtherKeys mode 2

	// GUI affordance.
	seqOSCProgressClear = "\x1b]9;4;0;\x07" // OSC 9;4;0 — ConEmu/Ghostty progress remove

	// Mouse modes.
	seqMouseX10Off          = "\x1b[?9l"
	seqMouseNormalOff       = "\x1b[?1000l"
	seqMouseButtonOff       = "\x1b[?1002l"
	seqMouseAnyOff          = "\x1b[?1003l"
	seqMouseFmtUTF8Off      = "\x1b[?1005l"
	seqMouseFmtSGROff       = "\x1b[?1006l"
	seqMouseFmtUrxvtOff     = "\x1b[?1015l"
	seqMouseFmtSGRPixelsOff = "\x1b[?1016l"

	// Keyboard input modes.
	seqAppCursorKeysOff   = "\x1b[?1l"
	seqAppKeypadDECOff    = "\x1b[?66l"
	seqAppKeypadESCOff    = "\x1b>" // DECKPNM
	seqBackarrowOff       = "\x1b[?67l"
	seqFocusEventsOff     = "\x1b[?1004l"
	seqBracketedPasteOff  = "\x1b[?2004l"
	seqAltSendsEscapeOff  = "\x1b[?1039l"
	seqDisableKeyboardOff = "\x1b[2l"  // KAM (ANSI, no ?)
	seqInsertModeOff      = "\x1b[4l"  // IRM (ANSI, no ?)
	seqLinefeedModeOff    = "\x1b[20l" // LNM (ANSI, no ?)

	// Display modes.
	seqReverseColorsOff     = "\x1b[?5l"
	seqOriginModeOff        = "\x1b[?6l"
	seqWraparoundOn         = "\x1b[?7h" // default-on
	seqSlowScrollOff        = "\x1b[?4l"
	seqReverseWrapOff       = "\x1b[?45l"
	seqReverseWrapExtOff    = "\x1b[?1045l"
	seq132ColumnOff         = "\x1b[?3l"
	seqSyncOutputOff        = "\x1b[?2026l"
	seqGraphemeClusterOff   = "\x1b[?2027l"
	seqReportColorSchemeOff = "\x1b[?2031l"
	seqInBandSizeReportsOff = "\x1b[?2048l"

	// Layout.
	seqLeftRightMarginOff = "\x1b[?69l"
	seqScrollRegionReset  = "\x1b[r"
	seqCursorShapeDefault = "\x1b[0 q"

	// Charset.
	seqCharsetG0Ascii = "\x0f\x1b(B" // SI + ESC ( B

	// Alt screens.
	seqAltScreen47Off   = "\x1b[?47l"
	seqAltScreen1047Off = "\x1b[?1047l"
	seqAltScreen1049Off = "\x1b[?1049l"

	// Visual reset.
	seqSGRReset    = "\x1b[0m"
	seqCursorShow  = "\x1b[?25h"
	seqClearScreen = "\x1b[H\x1b[2J"
)

// parseCSIParamDefault returns the integer value of params, or def if params
// is empty / non-numeric / non-positive. Used by the streaming CSI parser
// to read kitty kbd pop counts (CSI < N u).
func parseCSIParamDefault(params []byte, def int) int {
	if len(params) == 0 {
		return def
	}
	n, err := strconv.Atoi(string(params))
	if err != nil || n <= 0 {
		return def
	}
	return n
}
