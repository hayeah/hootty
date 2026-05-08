package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/sessionpick"
	"github.com/hayeah/hootty/internal/shortid"
)

// errFZFNotFound is returned by runFZFPicker when the fzf binary
// isn't on PATH. Callers map it to exit code 2 with an install
// hint so the user knows what to do.
var errFZFNotFound = errors.New("fzf not found on PATH")

// errPickerCancelled is returned when the user dismissed the picker
// without choosing (Esc or Ctrl-C). The caller maps it to exit 130.
var errPickerCancelled = errors.New("picker cancelled")

// errPickerNoMatch is returned when fzf exits 1 (--exit-0 with a
// pre-seeded query that matched nothing). The caller maps it to
// exit 2 alongside the user-typed pattern.
var errPickerNoMatch = errors.New("no session matched")

// pickerOptions tunes the fzf invocation per subcommand. Verb appears
// in the prompt and header so the picker UI reflects what the user
// is about to do (attach / clone / kill / detach).
type pickerOptions struct {
	// Verb is used to build the prompt ("<verb>> ") and header
	// ("↑↓ select · enter <verb> · esc cancel"). Defaults to "attach".
	Verb string

	// AliveOnly filters dead sessions out of the candidate list
	// before id-prefix resolution and the fzf picker run. Set by
	// verbs where dead sessions are dead-ends (attach, detach) or
	// where dead-session noise in the picker isn't useful by default
	// (log, unless --all). --strict bypasses the picker entirely
	// and is unaffected.
	AliveOnly bool
}

// runFZFPicker shells out to fzf to pick one session from states.
// On success it returns the chosen session's key (the value of
// state.Session.Key, with the [...] brackets stripped from the line).
//
// preseed is passed as fzf --query=<preseed> --select-1 --exit-0
// when non-empty: a unique fuzzy hit auto-selects without UI flash,
// zero hits exit cleanly with errPickerNoMatch, and multiple hits
// drop into the picker pre-filtered.
//
// fzf opens /dev/tty itself for keyboard + display, so the caller's
// stdin/stdout/stderr stay untouched.
func runFZFPicker(states []sessionpick.SessionWithMeta, preseed string, opts pickerOptions) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errFZFNotFound
	}

	if len(states) == 0 {
		return "", errPickerNoMatch
	}

	verb := opts.Verb
	if verb == "" {
		verb = "attach"
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
		"--prompt=" + verb + "> ",
		"--header=↑↓ select · enter " + verb + " · esc cancel",
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

// resolveSessionKey is the shared session-resolve path for attach,
// clone, kill, and detach. It implements the routing matrix:
//
//   - --strict: id-prefix only. Server resolves for --remote, store
//     resolves locally. Empty arg + --strict is an error.
//   - 0 args + tty: load list, run fzf picker (no preseed)
//   - 0 args + no tty: error, exit 2
//   - 1 arg: try IDResolve client-side; on unique → return; on
//     ambiguous → surface (id beats fuzzy); on no-match → fzf with
//     --query=arg --select-1 --exit-0 (tty only).
//
// On error, the second return is the process exit code to use.
func resolveSessionKey(arg string, strict bool, remote *Remote, stateDir string, opts pickerOptions) (string, int, error) {
	if strict {
		if arg == "" {
			return "", 2, fmt.Errorf("--strict requires a session id")
		}
		if remote != nil {
			// Existing behavior: server resolves. Downstream HTTP call
			// will 404/409 if the id is bad and we'll surface that.
			return arg, 0, nil
		}
		store := session.NewStore(stateDir)
		state, err := store.Resolve(arg)
		if err != nil {
			return "", 2, err
		}
		return state.Session.Key, 0, nil
	}

	hasTTY := stdoutIsTTY()
	if arg == "" && !hasTTY {
		return "", 2, fmt.Errorf("no session id given; tty required for picker")
	}

	states, err := loadSessionList(remote, stateDir)
	if err != nil {
		return "", 1, err
	}
	if opts.AliveOnly {
		filtered := states[:0]
		for _, sm := range states {
			if sm.Alive {
				filtered = append(filtered, sm)
			}
		}
		states = filtered
	}
	if len(states) == 0 {
		if opts.AliveOnly {
			return "", 2, fmt.Errorf("no live sessions available")
		}
		return "", 2, fmt.Errorf("no sessions available")
	}

	if arg != "" {
		sm, err := sessionpick.IDResolve(states, arg)
		if err == nil {
			return sm.State.Session.Key, 0, nil
		}
		var amb *shortid.AmbiguousIDError
		if errors.As(err, &amb) {
			// Id beats fuzzy on collision: surface the existing
			// ambiguity error instead of falling through.
			return "", 2, err
		}
		// IDNotFoundError or IDTooShortError → fall through to fzf.
		if !hasTTY {
			return "", 2, fmt.Errorf("no session matched %q (tty required for picker)", arg)
		}
	}

	key, err := runFZFPicker(states, arg, opts)
	switch {
	case err == nil:
		return key, 0, nil
	case errors.Is(err, errFZFNotFound):
		return "", 2, fmt.Errorf("fzf not found on PATH; install fzf (`brew install fzf` / `apt install fzf`) or pass --strict <id>")
	case errors.Is(err, errPickerCancelled):
		return "", 130, fmt.Errorf("picker cancelled")
	case errors.Is(err, errPickerNoMatch):
		if arg != "" {
			return "", 2, fmt.Errorf("no session matched %q", arg)
		}
		return "", 2, fmt.Errorf("no session selected")
	default:
		return "", 1, err
	}
}

// loadSessionList loads the candidate sessions for the picker. For
// --remote it issues GET /sessions (which already returns the
// `alive` flag); locally it walks the state directory and probes each
// dir's flock for liveness.
func loadSessionList(remote *Remote, stateDir string) ([]sessionpick.SessionWithMeta, error) {
	if remote != nil {
		resp, err := httpClient(remote).Get("http://hoot/sessions")
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, remoteResponseError(resp)
		}
		var body struct {
			Sessions []struct {
				session.StateFile
				Alive bool `json:"alive"`
			} `json:"sessions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, err
		}
		out := make([]sessionpick.SessionWithMeta, 0, len(body.Sessions))
		for i := range body.Sessions {
			st := body.Sessions[i].StateFile
			out = append(out, sessionpick.SessionWithMeta{
				State: &st,
				Alive: body.Sessions[i].Alive,
				Host:  remote.display,
			})
		}
		return out, nil
	}

	store := session.NewStore(stateDir)
	states, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]sessionpick.SessionWithMeta, 0, len(states))
	for _, st := range states {
		out = append(out, sessionpick.SessionWithMeta{
			State: st,
			Alive: store.IsAlive(st.Session.Key),
			Host:  "local",
		})
	}
	return out, nil
}
