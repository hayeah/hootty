package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	session "github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/sessionpick"
)

// fakeFZF installs a shell script named "fzf" in a temp dir prepended
// to PATH. The script:
//   - writes its argv (one per line) to <dir>/argv
//   - copies stdin to <dir>/stdin
//   - if <dir>/exit-code exists, exits with that code
//   - otherwise, prints <dir>/picked-line (or, if missing, the first
//     stdin line) and exits 0
//
// Returns the directory so tests can read argv/stdin and seed
// picked-line / exit-code.
func fakeFZF(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fakeFZF uses /bin/sh; not portable to Windows")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
set -e
DIR="` + dir + `"
# Capture argv one per line.
: > "$DIR/argv"
for a in "$@"; do printf '%s\n' "$a" >> "$DIR/argv"; done
# Capture stdin.
cat > "$DIR/stdin"
# Optional exit override.
if [ -f "$DIR/exit-code" ]; then
  exit "$(cat "$DIR/exit-code")"
fi
# Print the seeded pick or fall back to the first input line.
if [ -f "$DIR/picked-line" ]; then
  cat "$DIR/picked-line"
else
  head -n1 "$DIR/stdin"
fi
`
	path := filepath.Join(dir, "fzf")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fzf shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func mkState(key, cwd string, argv []string) sessionpick.SessionWithMeta {
	return sessionpick.SessionWithMeta{
		State: &session.StateFile{
			Session: session.SessionState{
				Key:       key,
				CreatedAt: time.Unix(1700000000, 0),
				CWD:       cwd,
				Argv:      argv,
			},
		},
		Alive: true,
		Host:  "local",
	}
}

func TestRunFZFPicker_returnsKey(t *testing.T) {
	dir := fakeFZF(t)
	states := []sessionpick.SessionWithMeta{
		mkState("a3fabc", "/tmp/a", []string{"sh"}),
		mkState("k7qzzz", "/tmp/b", []string{"nvim"}),
	}
	// Seed the shim to "select" the second session.
	pickedLine := "2\t[k7qzzz]\t@local\t/tmp/b\tnvim\t\n"
	if err := os.WriteFile(filepath.Join(dir, "picked-line"), []byte(pickedLine), 0o644); err != nil {
		t.Fatal(err)
	}

	key, err := runFZFPicker(states, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != "k7qzzz" {
		t.Fatalf("got key %q, want k7qzzz", key)
	}

	// Argv assertions: hidden index column, no preseed flags.
	argv := readLines(t, filepath.Join(dir, "argv"))
	mustContain(t, argv, "--with-nth=2..")
	mustContain(t, argv, "--no-sort")
	mustContain(t, argv, "--delimiter=\t")
	for _, a := range argv {
		if strings.HasPrefix(a, "--query=") {
			t.Errorf("did not expect --query in argv when preseed empty, got %q", a)
		}
	}

	// Stdin assertions: tab-separated, leading 1-based index.
	stdin := readFile(t, filepath.Join(dir, "stdin"))
	if !strings.Contains(stdin, "1\t[a3fabc]\t@local\t/tmp/a\tsh\t") {
		t.Errorf("stdin missing first row, got:\n%s", stdin)
	}
	if !strings.Contains(stdin, "2\t[k7qzzz]\t@local\t/tmp/b\tnvim\t") {
		t.Errorf("stdin missing second row, got:\n%s", stdin)
	}
}

func TestRunFZFPicker_preseedAddsSelectAndExitFlags(t *testing.T) {
	dir := fakeFZF(t)
	states := []sessionpick.SessionWithMeta{mkState("a3fabc", "/x", []string{"sh"})}
	if err := os.WriteFile(filepath.Join(dir, "picked-line"), []byte("1\t[a3fabc]\t@local\t/x\tsh\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runFZFPicker(states, "nvim"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	argv := readLines(t, filepath.Join(dir, "argv"))
	mustContain(t, argv, "--query=nvim")
	mustContain(t, argv, "--select-1")
	mustContain(t, argv, "--exit-0")
}

func TestRunFZFPicker_exit130IsCancelled(t *testing.T) {
	dir := fakeFZF(t)
	if err := os.WriteFile(filepath.Join(dir, "exit-code"), []byte("130"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runFZFPicker([]sessionpick.SessionWithMeta{mkState("a3fabc", "/x", []string{"sh"})}, "")
	if !errors.Is(err, errPickerCancelled) {
		t.Fatalf("got %v, want errPickerCancelled", err)
	}
}

func TestRunFZFPicker_exit1IsNoMatch(t *testing.T) {
	dir := fakeFZF(t)
	if err := os.WriteFile(filepath.Join(dir, "exit-code"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runFZFPicker([]sessionpick.SessionWithMeta{mkState("a3fabc", "/x", []string{"sh"})}, "zzz")
	if !errors.Is(err, errPickerNoMatch) {
		t.Fatalf("got %v, want errPickerNoMatch", err)
	}
}

func TestRunFZFPicker_missingBinaryIsSentinel(t *testing.T) {
	// Empty PATH so fzf can't be found anywhere.
	t.Setenv("PATH", "")
	_, err := runFZFPicker([]sessionpick.SessionWithMeta{mkState("a3fabc", "/x", []string{"sh"})}, "")
	if !errors.Is(err, errFZFNotFound) {
		t.Fatalf("got %v, want errFZFNotFound", err)
	}
}

func TestRunFZFPicker_emptyStateListIsNoMatch(t *testing.T) {
	fakeFZF(t)
	_, err := runFZFPicker(nil, "")
	if !errors.Is(err, errPickerNoMatch) {
		t.Fatalf("got %v, want errPickerNoMatch", err)
	}
}

// --- helpers ---

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func readLines(t *testing.T, p string) []string {
	t.Helper()
	s := readFile(t, p)
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func mustContain(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Errorf("argv missing %q; got:\n%s", want, strings.Join(lines, "\n"))
}
