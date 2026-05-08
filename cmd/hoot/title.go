package main

import (
	"io"
	"strings"
	"sync/atomic"
)

// Terminal-title control sequences used during a `hoot attach`.
//
// hudTitleSet uses OSC 2 ("set window title") rather than OSC 0 so we
// leave the icon name alone — important for iTerm2 / Terminal.app where
// the icon name is shown in the dock / cmd-tab and the user may have
// set it deliberately. The String Terminator we pick is BEL (0x07)
// rather than ESC \ ; both are valid per ECMA-48 and BEL has the
// widest historical support across terminals.
//
// hudTitlePush / hudTitlePop use the xterm window-manipulation title
// stack (CSI 22;2t / CSI 23;2t). Modern xterm, iTerm2, Ghostty, kitty,
// Wezterm and Alacritty all implement it; on a terminal that doesn't,
// the sequences parse-and-drop without rendering and the worst that
// happens is the title stays whatever hoot set it to — the shell will
// reassert on next prompt redraw. No harm.
//
// All helpers return the raw bytes. Callers gate emission on the
// "stdout is a tty" check so we never spam control bytes into a
// scripted attach's pipe.

const (
	// CSI 22;2t — push the current window title to the terminal's
	// internal title stack. Symmetric with hudTitlePopBytes.
	hudTitlePushBytes = "\x1b[22;2t"

	// CSI 23;2t — pop the previously-pushed title back into place.
	hudTitlePopBytes = "\x1b[23;2t"
)

// hudTitleSetBytes returns an OSC 2 sequence that sets the window
// title to `s`. Any embedded BEL/ESC bytes are stripped first so a
// hostile (or just unlucky) HUD line can't terminate the OSC string
// early or splice in a different control sequence. Callers should
// pass a sessionpick.FormatHUD result directly — it is already plain
// text by construction.
func hudTitleSetBytes(s string) string {
	return "\x1b]2;" + sanitizeTitle(s) + "\x07"
}

// sanitizeTitle strips bytes that could prematurely terminate an OSC
// string or splice in a CSI sequence. We keep printable ASCII and any
// byte >= 0x20 except 0x7f (DEL); everything else (CR/LF/TAB/NUL/ESC/
// BEL/...) gets dropped.
func sanitizeTitle(s string) string {
	if !needsSanitize(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x7f:
			// drop DEL
		case r < 0x20:
			// drop control bytes (NUL, BEL, ESC, CR, LF, TAB, ...)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// hudState owns the HUD line for an attach session and the title-stack
// emit/pop bookkeeping. One instance per `hoot attach` invocation.
//
// The HUD line is consumed in two places — onAttach pushes and sets
// the terminal title, and runServerLoop prints it as a one-shot
// scrollback line at attach time — so we keep them on one struct
// rather than passing the bare string around.
type hudState struct {
	line   string
	isTTY  bool
	pushed atomic.Bool
}

func newHUDState(line string) *hudState {
	return &hudState{
		line:  line,
		isTTY: stdoutIsTTY(),
	}
}

// onAttach emits the title-stack push (once per hudState) followed by
// an OSC 2 set. Called from runAttachLoop after termios is raw and
// before the connect loop. No-ops when stdout isn't a tty or we don't
// have a HUD line (best-effort StateFile load missed).
func (h *hudState) onAttach(w io.Writer) {
	if h == nil || !h.isTTY || h.line == "" {
		return
	}
	if h.pushed.CompareAndSwap(false, true) {
		_, _ = io.WriteString(w, hudTitlePushBytes)
	}
	_, _ = io.WriteString(w, hudTitleSetBytes(h.line))
}

// onDetach emits the title-stack pop, but only if onAttach actually
// pushed. Safe to call from defers regardless of attach success.
func (h *hudState) onDetach(w io.Writer) {
	if h == nil || !h.isTTY {
		return
	}
	if h.pushed.Load() {
		_, _ = io.WriteString(w, hudTitlePopBytes)
	}
}

// Line returns the formatted HUD line, or "" if the state.json
// load missed (in which case callers suppress emission entirely).
func (h *hudState) Line() string {
	if h == nil {
		return ""
	}
	return h.line
}
