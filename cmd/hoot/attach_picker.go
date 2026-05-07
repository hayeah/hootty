package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hayeah/hootty/internal/sessionpick"
)

// errFZFNotFound is returned by runFZFPicker when the fzf binary
// isn't on PATH. cmdAttach maps it to exit code 2 with an install
// hint so the user knows what to do.
var errFZFNotFound = errors.New("fzf not found on PATH")

// errPickerCancelled is returned when the user dismissed the picker
// without choosing (Esc or Ctrl-C). The caller maps it to exit 130.
var errPickerCancelled = errors.New("picker cancelled")

// errPickerNoMatch is returned when fzf exits 1 (--exit-0 with a
// pre-seeded query that matched nothing). The caller maps it to
// exit 2 alongside the user-typed pattern.
var errPickerNoMatch = errors.New("no session matched")

// runFZFPicker shells out to fzf to pick one session from states.
// On success it returns the chosen session's key (the value of
// state.Session.Key, with the [...] brackets stripped from the line).
//
// preseed is passed as fzf --query=<preseed> --select-1 --exit-0
// when non-empty: a unique fuzzy hit auto-attaches without UI flash,
// zero hits exit cleanly with errPickerNoMatch, and multiple hits
// drop into the picker pre-filtered.
//
// fzf opens /dev/tty itself for keyboard + display, so the caller's
// stdin/stdout/stderr stay untouched.
func runFZFPicker(states []sessionpick.SessionWithMeta, preseed string) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errFZFNotFound
	}

	if len(states) == 0 {
		return "", errPickerNoMatch
	}

	var input strings.Builder
	for i, sm := range states {
		// 1-based index, then the formatted line. --with-nth=2..
		// hides the index from the matcher; it's purely visual.
		fmt.Fprintf(&input, "%d\t%s\n", i+1, sessionpick.Format(sm))
	}

	args := []string{
		"--ansi",
		"--no-sort",
		"--delimiter=\t",
		"--with-nth=2..",
		"--prompt=attach> ",
		"--header=↑↓ select · enter attach · esc cancel",
		"--height=40%",
		"--reverse",
		"--no-mouse",
	}
	if preseed != "" {
		args = append(args, "--query="+preseed, "--select-1", "--exit-0")
	}

	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			switch ee.ExitCode() {
			case 1:
				return "", errPickerNoMatch
			case 130:
				return "", errPickerCancelled
			}
		}
		return "", fmt.Errorf("fzf: %w", err)
	}

	// fzf echoes back the full selected line (with our leading index
	// column). Field 2 is `[id]`; strip the brackets.
	line := strings.TrimRight(string(out), "\n")
	parts := strings.Split(line, "\t")
	if len(parts) < 2 {
		return "", fmt.Errorf("fzf: malformed selection %q", line)
	}
	bracketed := parts[1]
	if !strings.HasPrefix(bracketed, "[") || !strings.HasSuffix(bracketed, "]") {
		return "", fmt.Errorf("fzf: unexpected id field %q", bracketed)
	}
	return bracketed[1 : len(bracketed)-1], nil
}
