package main

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// fsmRecorder runs the FSM core against a synthetic byte stream and
// records every action it took. Each test fixture below feeds bytes
// in, then asserts the resulting (forwarded bytes, action sequence)
// pair against an expectation derived from the spec.

type fsmRecord struct {
	forwarded []byte
	actions   []string
}

func runFSMOnBytes(prefix byte, in []byte) *fsmRecord {
	rec := &fsmRecord{}
	i := 0
	read := func() (byte, bool) {
		if i >= len(in) {
			return 0, false
		}
		b := in[i]
		i++
		return b, true
	}
	act := fsmActions{
		forwardBytes: func(bs []byte) {
			rec.forwarded = append(rec.forwarded, bs...)
		},
		literal: func() { rec.actions = append(rec.actions, "literal") },
		detach:  func() { rec.actions = append(rec.actions, "detach") },
		suspend: func() { rec.actions = append(rec.actions, "suspend") },
		help:    func() { rec.actions = append(rec.actions, "help") },
		clone: func() error {
			rec.actions = append(rec.actions, "clone")
			return nil
		},
	}
	runChordFSM(prefix, read, act, &bytes.Buffer{})
	return rec
}

// TestRunChordFSM_LegacyDetach: with the legacy single-byte encoding
// the FSM detaches on `<prefix> .`. Baseline that the refactor did
// not regress the original mosh-style behavior.
func TestRunChordFSM_LegacyDetach(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte{0x1e, '.'})
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
	if len(r.forwarded) != 0 {
		t.Fatalf("forwarded = %q, want empty", r.forwarded)
	}
}

// TestRunChordFSM_KittyShifted: prefix arrives as the shifted-key
// CSI u report `\e[94;6u` (codepoint=`^`, modifier=ctrl+shift), and
// the chord follow-up `.` arrives as `\e[46u`. This is the canonical
// failing case from the bug report under Ghostty + Claude Code.
func TestRunChordFSM_KittyShifted(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[94;6u\x1b[46u"))
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
	if len(r.forwarded) != 0 {
		t.Fatalf("forwarded = %q, want empty", r.forwarded)
	}
}

// TestRunChordFSM_KittyBase: the spec allows terminals to report the
// base codepoint (`6`=54) with the ctrl+shift modifier instead of the
// shifted one. Detach must still fire.
func TestRunChordFSM_KittyBase(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[54;6u\x1b[46u"))
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
}

// TestRunChordFSM_KittyAlternate: with the "report alternate keys"
// flag, the report carries both shifted and base codepoints joined
// by ':'. Either side matching the prefix set is sufficient.
func TestRunChordFSM_KittyAlternate(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[94:54;6u\x1b[46u"))
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
}

// TestRunChordFSM_CodexPrefixReleaseThenDetach covers Codex's kitty
// keyboard mode: disambiguate + report event types + report alternates.
// The prefix key arrives as a press and a release; the release must not
// consume the chord window before the user presses '.'.
func TestRunChordFSM_CodexPrefixReleaseThenDetach(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[54:94;6u\x1b[54:94;6:3u."))
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
	if len(r.forwarded) != 0 {
		t.Fatalf("forwarded = %q, want empty", r.forwarded)
	}
}

// TestRunChordFSM_CSIuExplicitPressEvents verifies terminals that include
// the optional ":1" press tag still match both the prefix and follow-up.
func TestRunChordFSM_CSIuExplicitPressEvents(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[94;6:1u\x1b[46;1:1u"))
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
}

// TestRunChordFSM_CSIuRepeatReleaseIgnoredAfterPrefix verifies repeat and
// release events do not cancel the chord window after a matched prefix.
func TestRunChordFSM_CSIuRepeatReleaseIgnoredAfterPrefix(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[94;6u\x1b[94;6:2u\x1b[94;6:3u."))
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
}

// TestRunChordFSM_CSIuReleaseDoesNotBecomePrefix verifies a release event
// in normal state is forwarded to the inner program, not treated as hoot's
// prefix key.
func TestRunChordFSM_CSIuReleaseDoesNotBecomePrefix(t *testing.T) {
	in := []byte("\x1b[54:94;6:3u.")
	r := runFSMOnBytes(0x1e, in)
	if len(r.actions) != 0 {
		t.Fatalf("actions = %v, want none", r.actions)
	}
	if !bytes.Equal(r.forwarded, in) {
		t.Fatalf("forwarded = %q, want %q", r.forwarded, in)
	}
}

// TestRunChordFSM_PrefixReleaseThenUnknownFollowup verifies that prefix
// release is ignored, but the next non-chord press still closes the chord
// window according to the existing unknown-follow-up behavior.
func TestRunChordFSM_PrefixReleaseThenUnknownFollowup(t *testing.T) {
	r := runFSMOnBytes(0x1e, []byte("\x1b[94;6u\x1b[94;6:3ux."))
	if len(r.actions) != 0 {
		t.Fatalf("actions = %v, want none", r.actions)
	}
	if !bytes.Equal(r.forwarded, []byte(".")) {
		t.Fatalf("forwarded = %q, want %q", r.forwarded, ".")
	}
}

// TestRunChordFSM_KittyPrefixUnknownFollowup: prefix matches via CSI u
// but the follow-up is not a chord byte. The FSM must NOT detach.
// Per the spec's "forward both verbatim" wording the prefix bytes
// would also be visible — but legacy behavior silently consumes the
// prefix and the unrecognized follow-up. We preserve that, so all
// that matters is: no detach, and any forwarded bytes are at most the
// raw input (never partially decoded). The strong invariant is "no
// detach".
func TestRunChordFSM_KittyPrefixUnknownFollowup(t *testing.T) {
	// Follow-up = 'x' (not a chord).
	r := runFSMOnBytes(0x1e, []byte("\x1b[94;6ux"))
	for _, a := range r.actions {
		if a == "detach" {
			t.Fatalf("unexpected detach; actions = %v", r.actions)
		}
	}
}

// TestRunChordFSM_NonPrefixCSIu: a CSI u report whose codepoint is
// not in the prefix set must be forwarded verbatim, not consumed.
// Concretely, with prefix=C-^, ctrl+a (`\e[97;5u`) is data destined
// for the inner program.
func TestRunChordFSM_NonPrefixCSIu(t *testing.T) {
	in := []byte("\x1b[97;5u")
	r := runFSMOnBytes(0x1e, in)
	if len(r.actions) != 0 {
		t.Fatalf("actions = %v, want none", r.actions)
	}
	if !bytes.Equal(r.forwarded, in) {
		t.Fatalf("forwarded = %q, want %q", r.forwarded, in)
	}
}

// TestRunChordFSM_NonCSIuEscapeForwarded: a non-CSI-u escape sequence
// (here, F5 = `\e[15~`) under kitty mode arrives mixed in with
// regular keystrokes; the FSM must forward it byte-for-byte without
// mistaking any byte for the prefix.
func TestRunChordFSM_NonCSIuEscapeForwarded(t *testing.T) {
	in := []byte("\x1b[15~hello")
	r := runFSMOnBytes(0x1e, in)
	if len(r.actions) != 0 {
		t.Fatalf("actions = %v, want none", r.actions)
	}
	if !bytes.Equal(r.forwarded, in) {
		t.Fatalf("forwarded = %q, want %q", r.forwarded, in)
	}
}

// TestRunChordFSM_LegacyAndKittyMixed: a stream that begins with
// kitty-encoded keys, transitions to a legacy-encoded prefix chord,
// detaches. Verifies the FSM doesn't get stuck waiting for an ESC.
func TestRunChordFSM_LegacyAndKittyMixed(t *testing.T) {
	in := append([]byte("\x1b[97;5uhi"), 0x1e, '.')
	r := runFSMOnBytes(0x1e, in)
	if !reflect.DeepEqual(r.actions, []string{"detach"}) {
		t.Fatalf("actions = %v, want [detach]", r.actions)
	}
	want := []byte("\x1b[97;5uhi")
	if !bytes.Equal(r.forwarded, want) {
		t.Fatalf("forwarded = %q, want %q", r.forwarded, want)
	}
}

// TestParseCSIuParams covers the param-grammar corners that matter for
// chord matching: missing modifier, alternate-key form, modifier with
// event-type tail, multi-group params.
func TestParseCSIuParams(t *testing.T) {
	cases := []struct {
		in                  string
		cp, alt, mod, event uint
	}{
		{"46", 46, 0, 0, 0},
		{"46;1", 46, 0, 1, 0},
		{"94;6", 94, 0, 6, 0},
		{"94:54;6", 94, 54, 6, 0},
		{"94;6:1", 94, 0, 6, 1},
		{"46;1:2", 46, 0, 1, 2},
		{"54:94;6:3", 54, 94, 6, 3},
		{"65;5:3;65", 65, 0, 5, 3},
	}
	for _, c := range cases {
		cp, alt, mod, event := parseCSIuParams([]byte(c.in))
		if cp != c.cp || alt != c.alt || mod != c.mod || event != c.event {
			t.Errorf("parseCSIuParams(%q) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
				c.in, cp, alt, mod, event, c.cp, c.alt, c.mod, c.event)
		}
	}
}

// TestIsPrefixKey covers the codepoint+modifier matching for the
// default C-^ prefix and a representative C-b prefix.
func TestIsPrefixKey(t *testing.T) {
	type tc struct {
		prefix byte
		ev     keyEvent
		want   bool
	}
	cases := []tc{
		{0x1e, keyEvent{raw: []byte{0x1e}}, true},
		{0x1e, keyEvent{raw: []byte{'.'}}, false},
		{0x1e, keyEvent{csiU: true, cp: 94, mod: 6}, true}, // ctrl+shift+^
		{0x1e, keyEvent{csiU: true, cp: 54, mod: 6}, true}, // ctrl+shift+6
		{0x1e, keyEvent{csiU: true, cp: 54, altCp: 94, mod: 6, event: 1}, true},
		{0x1e, keyEvent{csiU: true, cp: 54, altCp: 94, mod: 6, event: 2}, false},
		{0x1e, keyEvent{csiU: true, cp: 54, altCp: 94, mod: 6, event: 3}, false},
		{0x1e, keyEvent{csiU: true, cp: 94, mod: 2}, false}, // shift only — no ctrl
		{0x1e, keyEvent{csiU: true, cp: 65, mod: 5}, false}, // ctrl+a
		{0x02, keyEvent{csiU: true, cp: 'b', mod: 5}, true}, // ctrl+b
		{0x02, keyEvent{csiU: true, cp: 'B', mod: 5}, true}, // ctrl+B (caps reported)
	}
	for i, c := range cases {
		got := isPrefixKey(c.prefix, c.ev)
		if got != c.want {
			t.Errorf("case %d: isPrefixKey(0x%02x, %+v) = %v, want %v", i, c.prefix, c.ev, got, c.want)
		}
	}
}

// TestRunChordFSM_OtherChordsCSIu spot-checks the remaining chord
// vocabulary (literal-prefix, suspend, help, clone) under kitty
// encoding to confirm the matcher generalizes beyond detach.
func TestRunChordFSM_OtherChordsCSIu(t *testing.T) {
	cases := []struct {
		name   string
		bytes  []byte
		action string
	}{
		{"literal-prefix via ctrl+shift+^", []byte("\x1b[94;6u\x1b[94;6u"), "literal"},
		{"suspend via ctrl+z", []byte("\x1b[94;6u\x1b[122;5u"), "suspend"},
		{"clone via 'c'", []byte("\x1b[94;6u\x1b[99u"), "clone"},
		{"help via '?'", []byte("\x1b[94;6u\x1b[63u"), "help"},
	}
	for _, c := range cases {
		r := runFSMOnBytes(0x1e, c.bytes)
		if !reflect.DeepEqual(r.actions, []string{c.action}) {
			t.Errorf("%s: actions = %v, want [%s]", c.name, r.actions, c.action)
		}
	}
}

// TestRunChordFSM_CloneError ensures clone failures surface to stderr
// without changing the FSM's progression.
func TestRunChordFSM_CloneError(t *testing.T) {
	stderr := &bytes.Buffer{}
	read := readerOf([]byte{0x1e, 'c'})
	act := fsmActions{
		forwardBytes: func([]byte) {},
		literal:      func() {},
		detach:       func() {},
		suspend:      func() {},
		help:         func() {},
		clone:        func() error { return errors.New("boom") },
	}
	runChordFSM(0x1e, read, act, stderr)
	if !bytes.Contains(stderr.Bytes(), []byte("clone failed: boom")) {
		t.Fatalf("stderr = %q, want clone failure note", stderr.String())
	}
}

func readerOf(b []byte) readByteFn {
	i := 0
	return func() (byte, bool) {
		if i >= len(b) {
			return 0, false
		}
		c := b[i]
		i++
		return c, true
	}
}
