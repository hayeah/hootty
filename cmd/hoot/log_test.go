package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
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

func TestCmdLogRefusesCurrentSessionToStdout(t *testing.T) {
	stateDir := t.TempDir()
	writeLogFixture(t, stateDir, "self123", "seed-line\r\n")
	t.Setenv(hootSessionEnv, "self123")

	err := cmdLog([]string{"--state-dir", stateDir, "--strict", "--format", "plain", "self123"})
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("cmdLog err = %v, want exitError code 2", err)
	}
	for _, want := range []string{"sessions should be logged with care", "--output FILE", "self123"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestCmdLogOutputAllowsCurrentSession(t *testing.T) {
	stateDir := t.TempDir()
	writeLogFixture(t, stateDir, "self123", "seed-line\r\n")
	outPath := filepath.Join(t.TempDir(), "self.txt")
	t.Setenv(hootSessionEnv, "self123")

	stdout := captureStdout(t, func() {
		if err := cmdLog([]string{"--state-dir", stateDir, "--strict", "--format", "plain", "--output", outPath, "self123"}); err != nil {
			t.Fatalf("cmdLog --output: %v", err)
		}
	})
	if stdout != "" {
		t.Fatalf("--output wrote to stdout: %q", stdout)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(raw), "seed-line") {
		t.Fatalf("output missing recording content: %q", string(raw))
	}
}

func TestCmdLogOutputRejectsSourceRecording(t *testing.T) {
	stateDir := t.TempDir()
	logPath := writeLogFixture(t, stateDir, "self123", "seed-line\r\n")

	err := cmdLog([]string{"--state-dir", stateDir, "--strict", "--format", "plain", "--output", logPath, "self123"})
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("cmdLog err = %v, want exitError code 2", err)
	}
	if !strings.Contains(err.Error(), "must not overwrite the source recording") {
		t.Fatalf("error missing source overwrite message: %v", err)
	}
}

func TestCmdLogInsideSessionRefusesSelfAppendE2E(t *testing.T) {
	stateDir := shortTempDir(t)
	helper := withHootHelperExecutable(t)

	spawnTestSession(t, stateDir, "self123", []string{
		"bash", "-lc",
		fmt.Sprintf("printf 'seed-line\\r\\n'; %q log --state-dir %q --format plain --strict \"$HOOT_SESSION\"; printf 'status=%%s\\r\\n' \"$?\"; sleep 1", helper, stateDir),
	})
	waitSessionExited(t, stateDir, "self123")

	got := recordedOutput(t, stateDir, "self123")
	if count := strings.Count(got, "seed-line"); count != 1 {
		t.Fatalf("self log appears appended; seed-line count=%d output=%q", count, got)
	}
	if !strings.Contains(got, "sessions should be logged with care") {
		t.Fatalf("recorded output missing refusal: %q", got)
	}
	if !strings.Contains(got, "status=2") {
		t.Fatalf("recorded output missing exit status: %q", got)
	}
}

func TestCmdLogInsideSessionAllowsOtherSessionE2E(t *testing.T) {
	stateDir := shortTempDir(t)
	writeLogFixture(t, stateDir, "other1", "other-line\r\n")
	helper := withHootHelperExecutable(t)

	spawnTestSession(t, stateDir, "self123", []string{
		"bash", "-lc",
		fmt.Sprintf("%q log --state-dir %q --format plain --strict other1; printf 'status=%%s\\r\\n' \"$?\"; sleep 1", helper, stateDir),
	})
	waitSessionExited(t, stateDir, "self123")

	got := recordedOutput(t, stateDir, "self123")
	if !strings.Contains(got, "other-line") {
		t.Fatalf("recorded output missing other session log: %q", got)
	}
	if !strings.Contains(got, "status=0") {
		t.Fatalf("recorded output missing success status: %q", got)
	}
	if strings.Contains(got, "sessions should be logged with care") {
		t.Fatalf("other session log was refused: %q", got)
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

// TestCmdLog_AllFlagFlipsAliveOnly drives cmdLog through the picker
// path on a state-dir that contains only a dead session.
//
//   - default (no --all): the picker is alive-only, the dead session
//     gets filtered out, cmdLog returns "no live sessions" exit 2.
//   - --all: the dead session shows in the picker, the fzf shim picks
//     it, cmdLog reads its pty.hootty.log and prints the recorded
//     plain text.
//
// Together these prove the --all flag is wired through to
// pickerOptions.AliveOnly. Both --strict and the asciinema/vt
// variants take the same resolveSessionKey path, so this one test
// covers the flag-plumbing surface.
func TestCmdLog_AllFlagFlipsAliveOnly(t *testing.T) {
	fakeTTY(t, true)
	fakeFZF(t)

	stateDir := seedSessions(t, "deadrec1")
	writeLogFixture(t, stateDir, "deadrec1", "hello\r\n")

	// Without --all: alive-only picker filters out the dead session →
	// no candidates → "no live sessions" error.
	err := cmdLog([]string{"--state-dir", stateDir, "--format", "plain"})
	if err == nil {
		t.Fatal("expected error without --all, got nil")
	}
	if !strings.Contains(err.Error(), "no live sessions") {
		t.Errorf("err = %q, want 'no live sessions' substring", err.Error())
	}

	// With --all: the dead session shows in the picker, the shim picks
	// the first row, cmdLog reads its recording. Capture stdout so the
	// emitted plain text is observable.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	err = cmdLog([]string{"--state-dir", stateDir, "--format", "plain", "--all"})
	w.Close()
	if err != nil {
		t.Fatalf("cmdLog --all: %v", err)
	}
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "hello") {
		t.Errorf("expected 'hello' in plain log output, got %q", string(out))
	}
}

func writeLogFixture(t *testing.T, stateDir, key, payload string) string {
	t.Helper()
	keyDir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatalf("mkdir key dir: %v", err)
	}
	stateJSON := fmt.Sprintf(`{"session":{"key":%q,"argv":["bash"],"cwd":%q,"created_at":"2026-05-08T00:00:00Z","size":{"cols":80,"rows":24}}}`, key, stateDir)
	if err := os.WriteFile(filepath.Join(keyDir, "state.json"), []byte(stateJSON), 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	logPath := filepath.Join(keyDir, "pty.hootty.log")
	rec, err := session.NewRecorder(logPath, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Write([]byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return logPath
}

func recordedOutput(t *testing.T, stateDir, key string) string {
	t.Helper()
	events, err := session.ReadOutputEvents(filepath.Join(stateDir, key, "pty.hootty.log"), 0)
	if err != nil {
		t.Fatalf("ReadOutputEvents: %v", err)
	}
	var out strings.Builder
	for _, ev := range events {
		out.Write(ev.Data)
	}
	return out.String()
}

func waitSessionExited(t *testing.T, stateDir, key string) {
	t.Helper()
	statePath := filepath.Join(stateDir, key, "state.json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(statePath)
		if err == nil && strings.Contains(string(raw), `"state": "exited"`) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s to exit", key)
}

func spawnTestSession(t *testing.T, stateDir, key string, argv []string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("setsize: %v", err)
	}
	self, err := hootExecutable()
	if err != nil {
		t.Fatalf("hootExecutable: %v", err)
	}
	args := []string{
		"__session",
		"--state-dir", stateDir,
		"--key", key,
		"--cwd", cwd,
		"--",
	}
	args = append(args, argv...)
	cmd := exec.Command(self, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "HOOT_TEST_HELPER_PROCESS=1")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.ExtraFiles = []*os.File{master}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper session: %v", err)
	}
	_ = slave.Close()
	_ = master.Close()
	_ = cmd.Process.Release()

	sockPath := filepath.Join(stateDir, key, "rpc.sock")
	if err := waitForSocket(sockPath, 10*time.Second); err != nil {
		dumpSessionDebug(t, stateDir, key)
		t.Fatalf("wait for socket: %v", err)
	}
}

func dumpSessionDebug(t *testing.T, stateDir, key string) {
	t.Helper()
	for _, name := range []string{"session.log", "state.json"} {
		raw, err := os.ReadFile(filepath.Join(stateDir, key, name))
		if err == nil {
			t.Logf("%s:\n%s", name, raw)
		}
	}
}

func withHootHelperExecutable(t *testing.T) string {
	t.Helper()
	testExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	old := hootExecutable
	hootExecutable = func() (string, error) { return testExe, nil }
	t.Cleanup(func() { hootExecutable = old })
	return testExe
}

func TestMain(m *testing.M) {
	if os.Getenv("HOOT_TEST_HELPER_PROCESS") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(runHootTestHelper(os.Args[1:]))
}

func runHootTestHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing helper command")
		return 2
	}
	var err error
	switch args[0] {
	case "__session":
		err = cmdSession(args[1:])
	case "log":
		err = cmdLog(args[1:])
	default:
		err = fmt.Errorf("unexpected helper command %q", args[0])
	}
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintf(os.Stderr, "hoot %s: %v\n", args[0], ee.err)
			}
			return ee.code
		}
		fmt.Fprintf(os.Stderr, "hoot %s: %v\n", args[0], err)
		return 1
	}
	return 0
}
