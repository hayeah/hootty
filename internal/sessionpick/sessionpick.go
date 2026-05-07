// Package sessionpick formats session lines for the `hoot attach`
// fzf picker and resolves user input against a candidate list.
//
// The picker UI itself (exec.Command("fzf", ...)) lives in
// cmd/hoot/attach_picker.go; this package is the pure-data half so
// it stays trivially testable.
package sessionpick

import (
	"os"
	"strings"

	session "github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/shortid"
)

// SessionWithMeta bundles a state.json record with the two pieces of
// context that aren't on disk: liveness (from a flock probe locally
// or the server's `alive` field over the wire) and the host display
// (the literal "local" for local sessions, or the remote target's
// display string for `--remote` listings).
type SessionWithMeta struct {
	State *session.StateFile
	Alive bool
	Host  string
}

// Format renders one session as a tab-separated line for fzf's
// stdin. The picker prepends a 1-based index column; everything
// after that is what fzf actually matches against (we pass
// --with-nth=2.. to hide the index from the matcher).
//
// Field order:
//
//	[id]\t@host\tcwd\tcmd args\ttag
//
// where tag is one of `*attached` (attachments slice non-empty),
// `(dead)` (flock probe says no live process), or empty (alive but
// no attachments).
func Format(sm SessionWithMeta) string {
	if sm.State == nil {
		return ""
	}
	st := sm.State.Session

	host := sm.Host
	if host == "" {
		host = "local"
	}

	cwd := tildify(st.CWD)
	cmd := strings.Join(st.Argv, " ")

	var tag string
	switch {
	case !sm.Alive:
		tag = "(dead)"
	case len(st.Attachments) > 0:
		tag = "*attached"
	default:
		tag = ""
	}

	return strings.Join([]string{
		"[" + st.Key + "]",
		"@" + host,
		cwd,
		cmd,
		tag,
	}, "\t")
}

// HUDMaxCmdLen is the maximum number of runes from the joined argv
// surfaced in a HUD line. Beyond this we cut on a rune boundary and
// append a single "…" so the line stays within most terminal widths.
const HUDMaxCmdLen = 60

// FormatHUD renders a one-line, human-readable summary of a session
// for use in the terminal title and the `<prefix> ?` status chord.
// It uses the same field-extraction as Format but drops the picker
// markers (no `[id]` brackets, no `@local`, no `*attached` tag) and
// prefixes the brand owl. Truncates the argv at HUDMaxCmdLen runes.
//
// Field order:
//
//	🦉 <id> [@host] <cwd> [<cmd...>]
//
// `@host` is omitted when the session is local (host empty or
// "local"). Trailing `<cmd...>` is omitted when argv is empty.
//
// All fields are space-separated; no tabs, no control bytes. Safe
// to drop into an OSC 2 title sequence and to print as raw text.
func FormatHUD(sm SessionWithMeta) string {
	if sm.State == nil {
		return ""
	}
	st := sm.State.Session

	parts := []string{"🦉", st.Key}

	host := sm.Host
	if host != "" && host != "local" {
		parts = append(parts, "@"+host)
	}

	if cwd := tildify(st.CWD); cwd != "" {
		parts = append(parts, cwd)
	}

	if cmd := truncateRunes(strings.Join(st.Argv, " "), HUDMaxCmdLen); cmd != "" {
		parts = append(parts, cmd)
	}

	return strings.Join(parts, " ")
}

// truncateRunes returns s if its rune length is <= max, otherwise
// the first max-1 runes followed by "…" (so the total rune length
// stays <= max). max <= 0 returns "".
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// tildify replaces a leading $HOME with ~ for compactness.
// Best-effort: if $HOME isn't set, the path is returned unchanged.
func tildify(p string) string {
	home := os.Getenv("HOME")
	if home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// IDResolve runs the existing shortid prefix matcher over the keys
// in states. On unique hit it returns the matching SessionWithMeta.
// On miss / ambiguity it returns the same shortid sentinels callers
// already errors.As against (`*shortid.IDNotFoundError`,
// `*shortid.AmbiguousIDError`, `*shortid.IDTooShortError`).
//
// Callers in cmd/hoot/attach.go use the sentinel type to decide
// whether to fall through to the fzf picker:
//
//   - IDNotFoundError or IDTooShortError → fzf with --query=arg
//   - AmbiguousIDError → surface as-is (id beats fuzzy on collision)
func IDResolve(states []SessionWithMeta, arg string) (SessionWithMeta, error) {
	keys := make([]string, 0, len(states))
	for _, sm := range states {
		if sm.State != nil {
			keys = append(keys, sm.State.Session.Key)
		}
	}
	match, err := shortid.Resolve(arg, keys)
	if err != nil {
		return SessionWithMeta{}, err
	}
	for _, sm := range states {
		if sm.State != nil && sm.State.Session.Key == match {
			return sm, nil
		}
	}
	// Unreachable: shortid.Resolve only returns keys we passed in.
	return SessionWithMeta{}, &shortid.IDNotFoundError{Query: arg}
}
