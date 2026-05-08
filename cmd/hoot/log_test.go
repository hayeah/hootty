package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hayeah/hootty"
)

// TestCmdLogReplayPlain drives the vt|plain replay path end-to-end:
// record a tiny session via the production Recorder, run the
// `--format plain` snapshot path, and verify the visible text shows
// up.
func TestCmdLogReplayPlain(t *testing.T) {
	stateDir := t.TempDir()
	key := "abc123"
	keyDir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	logPath := filepath.Join(keyDir, "pty.hootty.log")
	rec, err := session.NewRecorder(logPath, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Write([]byte("hello\r\nworld\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	if err := runLogReplay(logPath, session.ReplayFormatPlain, &out); err != nil {
		t.Fatalf("runLogReplay: %v", err)
	}
	got := out.String()
	for _, want := range []string{"hello", "world"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain snapshot missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain snapshot contains escape sequences: %q", got)
	}
}

// TestCmdLogAsciinema verifies the asciicast transcode path:
// header carries width/height from the prelude resize and timestamp/
// command/title from state.json; output events carry the right
// payloads.
func TestCmdLogAsciinema(t *testing.T) {
	stateDir := t.TempDir()
	key := "abc123"
	keyDir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Synthesize state.json — minimal but enough to validate the
	// header passthrough.
	stateJSON := `{"session":{"key":"abc123","argv":["sh","-lc","echo hi"],"created_at":"2026-05-08T00:00:00Z","size":{"cols":120,"rows":40}}}`
	if err := os.WriteFile(filepath.Join(keyDir, "state.json"), []byte(stateJSON), 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	logPath := filepath.Join(keyDir, "pty.hootty.log")
	rec, err := session.NewRecorder(logPath, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.RecordResize(100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	if err := runLogAsciinema(logPath, filepath.Join(keyDir, "state.json"), &out); err != nil {
		t.Fatalf("runLogAsciinema: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header + output + resize), got %d: %q", len(lines), out.String())
	}

	// Header.
	var header session.AsciicastHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header parse: %v (line=%q)", err, lines[0])
	}
	if header.Version != 2 || header.Width != 120 || header.Height != 40 {
		t.Fatalf("header = %+v, want v2 120x40", header)
	}
	if header.Command != "sh -lc echo hi" || header.Title != "abc123" {
		t.Fatalf("header metadata = %+v, want from state.json", header)
	}

	// Find the output and resize events.
	var sawOutput, sawResize bool
	for _, ln := range lines[1:] {
		var ev []any
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("event parse: %v (line=%q)", err, ln)
		}
		if len(ev) != 3 {
			t.Fatalf("event %q has %d fields, want 3", ln, len(ev))
		}
		kind, _ := ev[1].(string)
		switch kind {
		case "o":
			if data, _ := ev[2].(string); data == "hello" {
				sawOutput = true
			}
		case "r":
			if geom, _ := ev[2].(string); geom == "100x30" {
				sawResize = true
			}
		}
	}
	if !sawOutput {
		t.Fatalf("missing output event with payload \"hello\" in %q", out.String())
	}
	if !sawResize {
		t.Fatalf("missing resize event 100x30 in %q", out.String())
	}
}

// TestResolveLogFormatDefaults verifies the tty/pipe defaults and
// invalid-flag error path.
func TestResolveLogFormatDefaults(t *testing.T) {
	orig := stdoutIsTTY
	defer func() { stdoutIsTTY = orig }()

	stdoutIsTTY = func() bool { return true }
	got, err := resolveLogFormat("")
	if err != nil || got != "vt" {
		t.Fatalf("tty default = %q, %v; want vt, nil", got, err)
	}

	stdoutIsTTY = func() bool { return false }
	got, err = resolveLogFormat("")
	if err != nil || got != "plain" {
		t.Fatalf("pipe default = %q, %v; want plain, nil", got, err)
	}

	for _, name := range []string{"vt", "plain", "asciinema"} {
		got, err := resolveLogFormat(name)
		if err != nil || got != name {
			t.Fatalf("explicit %q = %q, %v", name, got, err)
		}
	}

	if _, err := resolveLogFormat("html"); err == nil {
		t.Fatal("expected error on invalid format")
	}
}
