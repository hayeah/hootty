package main

import (
	"context"
	"fmt"
	"io"
)

// The detach FSM understands two encodings of the prefix key and chord
// follow-up:
//
//   - the legacy single byte (e.g. 0x1e for C-^), produced by terminals
//     that do not implement the kitty keyboard protocol; and
//   - the CSI u report (e.g. "\x1b[94;6u" for ctrl+shift+`^`), produced
//     by terminals that have been told to enable the protocol — typically
//     by an inner TUI such as Claude Code.
//
// Without the CSI u branch, hitting C-^ . under Ghostty + Claude Code is
// silently forwarded as input rather than detaching, because Claude
// flips the outer terminal into kitty mode and the chord arrives as
// "\x1b[94;6u\x1b[46u" instead of "\x1e.".

// chordCmd is one of the prefix-then-X actions runStdinFSM acts on.
type chordCmd int

const (
	chordNone chordCmd = iota
	chordDetach
	chordLiteral
	chordSuspend
	chordClone
)

// keyEvent is one logical key extracted from the input byte stream:
// either a single byte (csiU=false) or a parsed CSI u report.
type keyEvent struct {
	raw   []byte // exact bytes consumed from the input stream
	csiU  bool   // true iff this is a CSI u report
	cp    uint   // primary codepoint (first param of CSI u)
	altCp uint   // alternate codepoint after ':' (0 if absent)
	mod   uint   // modifier param (second param), 0 if absent
	event uint   // event tag after modifier ':' (0 if absent)
}

// readByteFn returns the next byte from the input stream, or (0, false)
// on EOF / context cancellation.
type readByteFn func() (byte, bool)

// readKeyEvent reads bytes until it has assembled one logical key.
// Non-ESC bytes are returned as one-byte raw events. ESC followed by
// '[' is parsed as a CSI sequence; if its final byte is 'u' the params
// are decoded into cp/altCp/mod. Any other ESC- or CSI-shaped sequence
// is returned with raw bytes only (csiU=false), so callers can forward
// them verbatim.
func readKeyEvent(read readByteFn) (keyEvent, bool) {
	b, ok := read()
	if !ok {
		return keyEvent{}, false
	}
	if b != 0x1b {
		return keyEvent{raw: []byte{b}}, true
	}
	// ESC: peek the next byte to decide CSI vs. bare-ESC vs. alt-key.
	b2, ok := read()
	if !ok {
		return keyEvent{raw: []byte{0x1b}}, true
	}
	if b2 != '[' {
		// ESC + non-'[' (alt-key, SS3, plain ESC followed by another
		// key, etc.) — forward verbatim.
		return keyEvent{raw: []byte{0x1b, b2}}, true
	}
	// CSI: read params/intermediates until we hit a final byte
	// (0x40..0x7E per ECMA-48).
	raw := []byte{0x1b, '['}
	var params []byte
	for {
		nb, ok := read()
		if !ok {
			return keyEvent{raw: raw}, true
		}
		raw = append(raw, nb)
		if nb >= 0x40 && nb <= 0x7E {
			if nb == 'u' {
				cp, altCp, mod, event := parseCSIuParams(params)
				return keyEvent{raw: raw, csiU: true, cp: cp, altCp: altCp, mod: mod, event: event}, true
			}
			return keyEvent{raw: raw}, true
		}
		params = append(params, nb)
	}
}

// parseCSIuParams parses the parameter bytes of a `CSI <params> u`
// report. Format: `<cp>[:<altCp>][;<mod>[:<event>][;<text>...]]`. Only
// the first two ';'-separated groups are meaningful for chord matching.
func parseCSIuParams(params []byte) (cp, altCp, mod, event uint) {
	groups := splitByte(params, ';')
	if len(groups) >= 1 {
		sub := splitByte(groups[0], ':')
		cp = parseUint(sub[0])
		if len(sub) >= 2 {
			altCp = parseUint(sub[1])
		}
	}
	if len(groups) >= 2 {
		sub := splitByte(groups[1], ':')
		mod = parseUint(sub[0])
		if len(sub) >= 2 {
			event = parseUint(sub[1])
		}
	}
	return
}

func splitByte(b []byte, sep byte) [][]byte {
	out := [][]byte{}
	start := 0
	for i, c := range b {
		if c == sep {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	out = append(out, b[start:])
	return out
}

func parseUint(b []byte) uint {
	var n uint
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + uint(c-'0')
	}
	return n
}

// modBits returns the kitty modifier bitmask. Kitty encodes modifier as
// (1 + bitmask) where bitmask is shift=1 alt=2 ctrl=4 super=8 …; an
// absent or zero param means "no modifiers".
func modBits(mod uint) uint {
	if mod == 0 {
		return 0
	}
	return mod - 1
}

const modCtrl = 4

const (
	csiUEventOmitted = 0
	csiUEventPress   = 1
	csiUEventRepeat  = 2
	csiUEventRelease = 3
)

func isCSIuPress(ev keyEvent) bool {
	return !ev.csiU || ev.event == csiUEventOmitted || ev.event == csiUEventPress
}

func isCSIuRepeatOrRelease(ev keyEvent) bool {
	return ev.csiU && (ev.event == csiUEventRepeat || ev.event == csiUEventRelease)
}

// prefixCodepoints returns the printable codepoints (uppercase + lower-
// case + shift partner where applicable) that, combined with ctrl, would
// produce the given control byte. The returned set is small and fixed
// per prefix byte; callers compare it against CSI u cp/altCp.
func prefixCodepoints(prefix byte) map[uint]bool {
	out := map[uint]bool{}
	switch {
	case prefix >= 0x01 && prefix <= 0x1a:
		// C-a..C-z. Accept either case (terminals may report either).
		ch := uint('a' + (prefix - 1))
		out[ch] = true
		out[ch-32] = true
	case prefix == 0x00:
		// C-@ / C-Space; on US, also Shift+2.
		out[uint('@')] = true
		out[uint('2')] = true
		out[uint(' ')] = true
	case prefix == 0x1c:
		out[uint('\\')] = true
	case prefix == 0x1d:
		out[uint(']')] = true
	case prefix == 0x1e:
		// C-^: shifted '^' (94) or base '6' (54) on US layout.
		out[uint('^')] = true
		out[uint('6')] = true
	case prefix == 0x1f:
		out[uint('_')] = true
		out[uint('-')] = true
	case prefix == 0x7f:
		// Backspace; usually plain DEL byte regardless of kitty mode.
	}
	return out
}

// isPrefixKey reports whether ev encodes the configured prefix key.
// Legacy single byte matches directly; a CSI u report matches if its
// codepoint (or alternate-key codepoint) is in the prefix set AND its
// modifier carries the ctrl bit.
func isPrefixKey(prefix byte, ev keyEvent) bool {
	if !ev.csiU {
		return len(ev.raw) == 1 && ev.raw[0] == prefix
	}
	if !isCSIuPress(ev) {
		return false
	}
	if modBits(ev.mod)&modCtrl == 0 {
		return false
	}
	cps := prefixCodepoints(prefix)
	if cps[ev.cp] {
		return true
	}
	if ev.altCp != 0 && cps[ev.altCp] {
		return true
	}
	return false
}

// matchChord classifies the chord follow-up event. The chord vocabulary
// is mosh-style: '.' detach, '^' literal-prefix, Ctrl-Z suspend, 'c'
// clone. Each is recognized in either legacy or CSI u form.
func matchChord(ev keyEvent) chordCmd {
	if !ev.csiU {
		if len(ev.raw) != 1 {
			return chordNone
		}
		switch ev.raw[0] {
		case '.':
			return chordDetach
		case '^':
			return chordLiteral
		case 0x1a:
			return chordSuspend
		case 'c':
			return chordClone
		}
		return chordNone
	}
	if !isCSIuPress(ev) {
		return chordNone
	}
	bits := modBits(ev.mod)
	hasCtrl := bits&modCtrl != 0
	// For each chord, accept the codepoint (and shift-partner where
	// relevant) under the right modifier expectation.
	cp := ev.cp
	altCp := ev.altCp
	any := func(want ...uint) bool {
		for _, w := range want {
			if cp == w || (altCp != 0 && altCp == w) {
				return true
			}
		}
		return false
	}
	switch {
	case any(46) && !hasCtrl:
		// '.' (46). Plain key, no ctrl. Shift+. = '>' would be cp 62.
		return chordDetach
	case any(94, 54) && hasCtrl:
		// '^' or '6' with ctrl — same encoding as the prefix; sending
		// it as a follow-up means "literal prefix byte".
		return chordLiteral
	case any(122, 90) && hasCtrl:
		// Ctrl-Z under kitty: codepoint 'z'/'Z' with ctrl modifier.
		return chordSuspend
	case any(99, 67) && !hasCtrl:
		// 'c' or 'C' without ctrl.
		return chordClone
	}
	return chordNone
}

// fsmActions abstracts the side effects of the chord FSM so the core
// loop is testable without a live session.
type fsmActions struct {
	forwardBytes func([]byte)
	literal      func()
	detach       func()
	suspend      func()
	clone        func() error
}

// runChordFSM is the testable core of runStdinFSM: it pulls logical
// keys from `read`, classifies them against `prefix`, and invokes the
// matching action. Returns when `read` reports EOF/cancellation.
func runChordFSM(prefix byte, read readByteFn, act fsmActions, stderr io.Writer) {
	const (
		stateNormal    = 0
		stateAfterPref = 1
	)
	state := stateNormal
	for {
		ev, ok := readKeyEvent(read)
		if !ok {
			return
		}
		switch state {
		case stateNormal:
			if isPrefixKey(prefix, ev) {
				state = stateAfterPref
				continue
			}
			act.forwardBytes(ev.raw)
		case stateAfterPref:
			if isCSIuRepeatOrRelease(ev) {
				continue
			}
			cmd := matchChord(ev)
			switch cmd {
			case chordDetach:
				act.detach()
				return
			case chordLiteral:
				act.literal()
			case chordSuspend:
				act.suspend()
			case chordClone:
				if err := act.clone(); err != nil && stderr != nil {
					fmt.Fprintf(stderr, "\r\n\x1b[2m[hoot: clone failed: %v]\x1b[0m\r\n", err)
				}
			default:
				// Unknown follow-up: silent ignore (matches legacy
				// mosh-style behavior). The bytes were already
				// consumed by readKeyEvent.
			}
			state = stateNormal
		}
	}
}

// readByteFromChan adapts a buffered byte channel + ctx to readByteFn.
// Used by runStdinFSM; lifted here so tests can stay decoupled from
// os.Stdin.
func readByteFromChan(ctx context.Context, ch <-chan byteOrErr) readByteFn {
	return func() (byte, bool) {
		select {
		case <-ctx.Done():
			return 0, false
		case rr, ok := <-ch:
			if !ok || rr.err != nil {
				return 0, false
			}
			return rr.b, true
		}
	}
}

type byteOrErr struct {
	b   byte
	err error
}
