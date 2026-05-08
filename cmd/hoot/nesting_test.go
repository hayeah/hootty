package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestErrIfNestedHootSession checks the guard helper itself: nil
// when env is empty, non-nil with the tmux-shaped message when set.
func TestErrIfNestedHootSession(t *testing.T) {
	t.Setenv(hootSessionEnv, "")
	if err := errIfNestedHootSession("attach"); err != nil {
		t.Fatalf("empty env should not error, got %v", err)
	}

	t.Setenv(hootSessionEnv, "abc123")
	err := errIfNestedHootSession("attach")
	if err == nil {
		t.Fatalf("set env should error")
	}
	msg := err.Error()
	for _, want := range []string{
		"sessions should be nested with care",
		"unset $" + hootSessionEnv,
		"abc123",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

// TestSanitizeChildEnvPublishesHootSession proves the spawned shell
// learns its own session key via the env, and that any inherited
// HOOT_SESSION from a (forbidden) outer hoot is replaced rather than
// shadowed.
func TestSanitizeChildEnvPublishesHootSession(t *testing.T) {
	parent := []string{
		"FOO=bar",
		"HOOT_SESSION=outer",
		"TMUX=/tmp/tmux-1000/default,1234,0",
		"TERM=tmux-256color",
	}
	got := sanitizeChildEnv(parent, "innerKey")
	if findEnv(got, "HOOT_SESSION") != "innerKey" {
		t.Fatalf("HOOT_SESSION = %q, want innerKey (full env=%v)",
			findEnv(got, "HOOT_SESSION"), got)
	}
	// Belt-and-braces: only one HOOT_SESSION entry, not two.
	count := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "HOOT_SESSION=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("HOOT_SESSION count = %d, want 1 (env=%v)", count, got)
	}
	// TMUX/TERM behaviors unchanged.
	if findEnv(got, "TMUX") != "" {
		t.Errorf("TMUX should be stripped, got %q", findEnv(got, "TMUX"))
	}
	if findEnv(got, "TERM") != "xterm-256color" {
		t.Errorf("TERM = %q, want xterm-256color", findEnv(got, "TERM"))
	}
	if findEnv(got, "FOO") != "bar" {
		t.Errorf("unrelated FOO got dropped, env=%v", got)
	}
}

// TestSanitizeChildEnvEmptyKeyOmits proves an empty session key (e.g.
// from a future code path that doesn't have a key yet) does not emit
// HOOT_SESSION= — better silent than a misleading empty marker.
func TestSanitizeChildEnvEmptyKeyOmits(t *testing.T) {
	got := sanitizeChildEnv([]string{"FOO=bar"}, "")
	if findEnv(got, "HOOT_SESSION") != "" {
		t.Fatalf("expected no HOOT_SESSION when key is empty, got %v", got)
	}
}

func findEnv(env []string, name string) string {
	prefix := name + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// TestCmdAttachRefusesWhenNested checks the wiring: cmdAttach prints
// its own "hoot attach: <msg>" and returns exit 2 when HOOT_SESSION
// is set in the environment.
func TestCmdAttachRefusesWhenNested(t *testing.T) {
	t.Setenv(hootSessionEnv, "outerSession")
	stderr := captureStderr(t, func() {
		code := cmdAttach([]string{"--state-dir", t.TempDir(), "--strict", "noSuchKey"})
		if code != 2 {
			t.Fatalf("cmdAttach exit = %d, want 2", code)
		}
	})
	if !strings.Contains(stderr, "hoot attach:") ||
		!strings.Contains(stderr, "sessions should be nested with care") ||
		!strings.Contains(stderr, "unset $HOOT_SESSION") {
		t.Fatalf("stderr missing expected message: %q", stderr)
	}
}

// TestCmdShellRefusesWhenNested asserts the bare `hoot` / `hoot @host`
// path bails before any spawn or remote dial.
func TestCmdShellRefusesWhenNested(t *testing.T) {
	t.Setenv(hootSessionEnv, "outerSession")
	err := cmdShell([]string{"--state-dir", t.TempDir()})
	if err == nil {
		t.Fatalf("cmdShell should error when nested")
	}
	if !strings.Contains(err.Error(), "sessions should be nested with care") ||
		!strings.Contains(err.Error(), "unset $HOOT_SESSION") {
		t.Fatalf("error missing expected message: %v", err)
	}
}

// TestCmdRunAttachRefusesWhenNested asserts `hoot run --attach` is
// blocked when nested. Without --attach, cmdRun runs to completion
// (covered separately).
func TestCmdRunAttachRefusesWhenNested(t *testing.T) {
	t.Setenv(hootSessionEnv, "outerSession")
	err := cmdRun([]string{
		"--attach", "--state-dir", t.TempDir(),
		"--", "true",
	})
	if err == nil {
		t.Fatalf("cmdRun --attach should error when nested")
	}
	if !strings.Contains(err.Error(), "sessions should be nested with care") {
		t.Fatalf("error missing expected message: %v", err)
	}
}

// TestCmdRunWithoutAttachAllowedWhenNested confirms the guard is
// scoped to attach paths: a fire-and-forget `hoot run` (no --attach)
// stays allowed inside a session, mirroring tmux's relaxed posture
// for non-takeover commands.
func TestCmdRunWithoutAttachAllowedWhenNested(t *testing.T) {
	t.Setenv(hootSessionEnv, "outerSession")
	stateDir := shortTempDir(t)
	cwdDir, err := os.MkdirTemp(stateDir, "cwd-")
	if err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	withFakeHootExecutable(t)
	withNonTTYStdin(t)

	out := captureStdout(t, func() {
		if err := cmdRun([]string{
			"--state-dir", stateDir,
			"--key", "innerKey",
			"--cwd", cwdDir,
			"--", "fakecmd", "arg1",
		}); err != nil {
			t.Fatalf("cmdRun without --attach refused while nested: %v", err)
		}
	})
	if strings.TrimSpace(out) != "innerKey" {
		t.Fatalf("output = %q, want innerKey", out)
	}
}

// TestCmdListAllowedWhenNested is a sanity check on a representative
// read-only command: even with HOOT_SESSION set it should not refuse.
// The guard is opt-in (only the three takeover paths call it), so all
// we have to prove is "the refusal message doesn't surface".
func TestCmdListAllowedWhenNested(t *testing.T) {
	t.Setenv(hootSessionEnv, "outerSession")
	stateDir := shortTempDir(t)
	out := captureStdout(t, func() {
		err := cmdList([]string{"--state-dir", stateDir})
		if err != nil && strings.Contains(err.Error(), "sessions should be nested with care") {
			t.Fatalf("cmdList refused as nested: %v", err)
		}
	})
	if strings.Contains(out, "sessions should be nested with care") {
		t.Fatalf("cmdList stdout shows nesting refusal: %q", out)
	}
}

// captureStderr is a stderr twin of captureStdout (defined in
// list_test.go). cmdAttach prints its own error envelope to stderr
// before returning, so we can't read it via the returned error alone.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	fn()
	_ = w.Close()
	return string(<-done)
}
