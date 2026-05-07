package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	session "github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/shortid"
)

// fakeTTY swaps stdoutIsTTY to return v for the test. Required because
// the real Stdout is never a tty under `go test`, so the picker code
// path would always error out otherwise.
func fakeTTY(t *testing.T, v bool) {
	t.Helper()
	orig := stdoutIsTTY
	stdoutIsTTY = func() bool { return v }
	t.Cleanup(func() { stdoutIsTTY = orig })
}

// seedSessions writes state.json under stateDir/<key>/ for each key
// in keys and returns the dir. The minimum content needed by
// store.List() is a session with the right key + a CWD; we don't
// touch flock so IsAlive returns false (sessions appear "(dead)" but
// are listed, which is what we want for the resolver).
func seedSessions(t *testing.T, keys ...string) string {
	t.Helper()
	stateDir := shortTempDir(t)
	for _, k := range keys {
		dir := filepath.Join(stateDir, k)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", k, err)
		}
		sf := session.StateFile{Session: session.SessionState{
			Key:  k,
			CWD:  dir,
			Argv: []string{"sh"},
		}}
		if err := writeJSON(filepath.Join(dir, "state.json"), sf); err != nil {
			t.Fatalf("write %s: %v", k, err)
		}
	}
	return stateDir
}

// TestResolveSessionKey_StrictRequiresArg covers the --strict + empty
// arg branch for every verb — the error message is shared but the
// downstream behavior depends on this guard not being skipped.
func TestResolveSessionKey_StrictRequiresArg(t *testing.T) {
	stateDir := seedSessions(t, "abc123")
	for _, verb := range []string{"attach", "clone", "kill", "detach"} {
		t.Run(verb, func(t *testing.T) {
			_, code, err := resolveSessionKey("", true, nil, stateDir, pickerOptions{Verb: verb})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if code != 2 {
				t.Errorf("code = %d, want 2", code)
			}
			if !strings.Contains(err.Error(), "--strict requires") {
				t.Errorf("err = %q, want '--strict requires' substring", err.Error())
			}
		})
	}
}

// TestResolveSessionKey_StrictResolvesPrefixLocally verifies the
// strict path goes through store.Resolve and returns the full key
// without launching fzf.
func TestResolveSessionKey_StrictResolvesPrefixLocally(t *testing.T) {
	t.Setenv("PATH", "")          // ensure fzf is unreachable
	fakeTTY(t, true)              // would normally trigger picker fallback
	stateDir := seedSessions(t, "abc123def")

	key, code, err := resolveSessionKey("abc", true, nil, stateDir, pickerOptions{Verb: "kill"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if key != "abc123def" {
		t.Errorf("key = %q, want abc123def", key)
	}
}

// TestResolveSessionKey_NoArgNonTTY exits 2 because the picker would
// need a tty.
func TestResolveSessionKey_NoArgNonTTY(t *testing.T) {
	stateDir := seedSessions(t, "abc123")
	fakeTTY(t, false)

	_, code, err := resolveSessionKey("", false, nil, stateDir, pickerOptions{Verb: "clone"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), "tty required for picker") {
		t.Errorf("err = %q, want 'tty required for picker' substring", err.Error())
	}
}

// TestResolveSessionKey_VerbThreadsThroughToFZF makes sure each verb
// shows up in the fzf prompt the picker invocation builds. This is
// the surface the user sees ("clone> ", "kill> ", "detach> ") and
// the test guards against accidentally regressing back to "attach> "
// for everything.
func TestResolveSessionKey_VerbThreadsThroughToFZF(t *testing.T) {
	for _, verb := range []string{"clone", "kill", "detach"} {
		t.Run(verb, func(t *testing.T) {
			dir := fakeFZF(t)
			fakeTTY(t, true)
			stateDir := seedSessions(t, "a3fabc1", "k7qzzz9")
			// Seed shim to "pick" the second row so we exercise the
			// happy-path return.
			pickedLine := "2\t[k7qzzz9]\t@local\t/x\tsh\t\n"
			if err := os.WriteFile(filepath.Join(dir, "picked-line"), []byte(pickedLine), 0o644); err != nil {
				t.Fatal(err)
			}

			key, code, err := resolveSessionKey("", false, nil, stateDir, pickerOptions{Verb: verb})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if code != 0 || key != "k7qzzz9" {
				t.Fatalf("got (%q, %d), want (k7qzzz9, 0)", key, code)
			}

			argv := readLines(t, filepath.Join(dir, "argv"))
			mustContain(t, argv, "--prompt="+verb+"> ")
			mustContain(t, argv, "--header=↑↓ select · enter "+verb+" · esc cancel")
			// No preseed → no --query / --select-1 / --exit-0.
			for _, a := range argv {
				if strings.HasPrefix(a, "--query=") || a == "--select-1" || a == "--exit-0" {
					t.Errorf("did not expect %q in argv with empty preseed", a)
				}
			}
		})
	}
}

// TestResolveSessionKey_IDPrefixMissPreseedsFZF verifies the
// "pattern arg + tty + miss" path — the user typed "nvim" as a
// session arg, no id prefix matches, and the picker re-runs with
// --query=nvim --select-1 --exit-0 so a unique fuzzy hit
// auto-targets without a UI flash.
func TestResolveSessionKey_IDPrefixMissPreseedsFZF(t *testing.T) {
	dir := fakeFZF(t)
	fakeTTY(t, true)
	stateDir := seedSessions(t, "a3fabc1", "k7qzzz9")
	pickedLine := "2\t[k7qzzz9]\t@local\t/x\tsh\t\n"
	if err := os.WriteFile(filepath.Join(dir, "picked-line"), []byte(pickedLine), 0o644); err != nil {
		t.Fatal(err)
	}

	key, code, err := resolveSessionKey("nvim", false, nil, stateDir, pickerOptions{Verb: "clone"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if code != 0 || key != "k7qzzz9" {
		t.Fatalf("got (%q, %d), want (k7qzzz9, 0)", key, code)
	}

	argv := readLines(t, filepath.Join(dir, "argv"))
	mustContain(t, argv, "--query=nvim")
	mustContain(t, argv, "--select-1")
	mustContain(t, argv, "--exit-0")
}

// TestResolveSessionKey_IDPrefixMatchSkipsPicker verifies that a
// uniquely-matching id-prefix arg returns directly without invoking
// fzf at all (so scripted use that happens to be on a tty doesn't
// flash a picker for valid prefixes).
func TestResolveSessionKey_IDPrefixMatchSkipsPicker(t *testing.T) {
	dir := fakeFZF(t)
	fakeTTY(t, true)
	stateDir := seedSessions(t, "a3fabc1", "k7qzzz9")

	key, code, err := resolveSessionKey("a3f", false, nil, stateDir, pickerOptions{Verb: "kill"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if code != 0 || key != "a3fabc1" {
		t.Fatalf("got (%q, %d), want (a3fabc1, 0)", key, code)
	}

	// fzf shim never ran — no argv file got written.
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Errorf("fzf was invoked but should have been bypassed; argv stat err = %v", err)
	}
}

// TestResolveSessionKey_AmbiguousIDBeatsFuzzy verifies that an
// ambiguous prefix surfaces shortid.AmbiguousIDError directly instead
// of falling through to the picker — the contract is "id beats
// fuzzy on collision".
func TestResolveSessionKey_AmbiguousIDBeatsFuzzy(t *testing.T) {
	fakeFZF(t) // shim is on PATH but should never be invoked
	fakeTTY(t, true)
	stateDir := seedSessions(t, "abc111", "abc222")

	_, code, err := resolveSessionKey("abc", false, nil, stateDir, pickerOptions{Verb: "detach"})
	if err == nil {
		t.Fatal("expected ambiguous error, got nil")
	}
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	var amb *shortid.AmbiguousIDError
	if !errors.As(err, &amb) {
		t.Errorf("err = %T %q, want *shortid.AmbiguousIDError", err, err.Error())
	}
}

// TestResolveSessionKey_PickerCancelMaps130 maps the fzf 130 exit
// (Esc / Ctrl-C) to process exit code 130, mirroring attach.
func TestResolveSessionKey_PickerCancelMaps130(t *testing.T) {
	dir := fakeFZF(t)
	if err := os.WriteFile(filepath.Join(dir, "exit-code"), []byte("130"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeTTY(t, true)
	stateDir := seedSessions(t, "a3fabc1")

	_, code, err := resolveSessionKey("", false, nil, stateDir, pickerOptions{Verb: "clone"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code != 130 {
		t.Errorf("code = %d, want 130", code)
	}
}

// TestResolveSessionKey_FZFExit1NoMatchMaps2 maps fzf's 1 (no match
// under --exit-0) to process exit 2 with the user-typed pattern in
// the error message.
func TestResolveSessionKey_FZFExit1NoMatchMaps2(t *testing.T) {
	dir := fakeFZF(t)
	if err := os.WriteFile(filepath.Join(dir, "exit-code"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeTTY(t, true)
	stateDir := seedSessions(t, "a3fabc1")

	_, code, err := resolveSessionKey("zzz", false, nil, stateDir, pickerOptions{Verb: "kill"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), `"zzz"`) {
		t.Errorf("err = %q, want %q substring", err.Error(), `"zzz"`)
	}
}
