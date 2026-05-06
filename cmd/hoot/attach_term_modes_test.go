package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResetTermModesOrder(t *testing.T) {
	want := strings.Join([]string{
		seqFocusEventsOff,
		seqBracketedPasteOff,
		seqMouseSGROff,
		seqMouseX10Off,
		seqAltScreenOff,
		seqSGRReset,
		seqCursorShow,
		seqClearScreen,
	}, "")

	if resetTermModes != want {
		t.Fatalf("resetTermModes = %q, want %q", resetTermModes, want)
	}
}

func TestEmitDetachWritesModeResetBeforeBanner(t *testing.T) {
	var out bytes.Buffer
	emitDetach(&out, attachLabel{Session: "session", Host: "host"}, nil)

	want := resetTermModes + "\r\n[disconnected. session @ host]\r\n"
	if out.String() != want {
		t.Fatalf("emitDetach = %q, want %q", out.String(), want)
	}
}

func TestTerminalModeTrackerPushesSnapshotKittyState(t *testing.T) {
	var out bytes.Buffer
	tracker := &terminalModeTracker{}

	if err := tracker.pushKittyKbdForSnapshot(&out, []byte("screen\x1b[=1;1u")); err != nil {
		t.Fatalf("pushKittyKbdForSnapshot: %v", err)
	}
	if out.String() != seqKittyKbdPushEmpty {
		t.Fatalf("snapshot prefix = %q, want %q", out.String(), seqKittyKbdPushEmpty)
	}

	var detach bytes.Buffer
	emitDetach(&detach, attachLabel{Session: "session", Host: "host"}, tracker)
	wantPrefix := seqKittyKbdPop + resetTermModes
	if !strings.HasPrefix(detach.String(), wantPrefix) {
		t.Fatalf("detach = %q, want prefix %q", detach.String(), wantPrefix)
	}
}

func TestTerminalModeTrackerBalancesLiveKittyPushPop(t *testing.T) {
	tracker := &terminalModeTracker{}
	tracker.observe([]byte("\x1b[>1u"))
	if got := tracker.detachReset(); got != seqKittyKbdPop {
		t.Fatalf("detach reset after push = %q, want %q", got, seqKittyKbdPop)
	}

	tracker.observe([]byte("\x1b[<u"))
	if got := tracker.detachReset(); got != "" {
		t.Fatalf("detach reset after matching pop = %q, want empty", got)
	}
}

func TestTerminalModeTrackerLivePopCanConsumeSnapshotFrame(t *testing.T) {
	var out bytes.Buffer
	tracker := &terminalModeTracker{}
	if err := tracker.pushKittyKbdForSnapshot(&out, []byte("\x1b[=1;1u")); err != nil {
		t.Fatalf("pushKittyKbdForSnapshot: %v", err)
	}

	tracker.observe([]byte("\x1b[<u"))
	if got := tracker.detachReset(); got != "" {
		t.Fatalf("detach reset after live pop consumed snapshot frame = %q, want empty", got)
	}
}

func TestTerminalModeTrackerObservesSplitKittyPush(t *testing.T) {
	tracker := &terminalModeTracker{}
	tracker.observe([]byte("\x1b[>"))
	tracker.observe([]byte("1u"))
	if got := tracker.detachReset(); got != seqKittyKbdPop {
		t.Fatalf("detach reset after split push = %q, want %q", got, seqKittyKbdPop)
	}
}

func TestTerminalModeTrackerObservesModifyOtherKeys(t *testing.T) {
	tracker := &terminalModeTracker{}
	tracker.observe([]byte("\x1b[>4;2m"))
	if got := tracker.detachReset(); got != seqModifyOtherKeysOff {
		t.Fatalf("detach reset after modifyOtherKeys = %q, want %q", got, seqModifyOtherKeysOff)
	}

	tracker.observe([]byte("\x1b[>4;0m"))
	if got := tracker.detachReset(); got != "" {
		t.Fatalf("detach reset after modifyOtherKeys reset = %q, want empty", got)
	}
}
