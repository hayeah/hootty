package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hayeah/hootty"
)

// cmdLog implements `hoot log [<target>] [--format vt|plain|asciinema]`.
//
// Routes:
//
//   - --format vt|plain → replay all binary frames into a fresh
//     libghostty.Terminal, then dump the formatter snapshot. Default
//     when stdout is a tty (vt) or a pipe (plain).
//
//   - --format asciinema → stream-transcode the binary log to an
//     asciicast v2 JSONL stream. Header metadata
//     (timestamp/command/title) comes from <state-dir>/<key>/state.json;
//     width/height come from the recording's prelude resize frame.
//
// Local-only in v1 — no --remote flag. The session must exist on disk
// at <state-dir>/<key>/. Use the existing fzf picker for resolution
// (matches `hoot attach`).
func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  hoot log [<id-or-prefix>] [--format vt|plain|asciinema] [--state-dir <d>] [--strict] [--all]

View a recorded session's PTY log. With no positional argument and on
a tty, an interactive fzf picker resolves the session (matches `+"`hoot attach`"+`).
Picker and id-prefix resolution show live sessions only by default;
pass `+"`--all`"+` to include exited sessions (their recordings remain
readable). `+"`--strict`"+` is unaffected — id-prefix resolution against
the on-disk store always sees every session.

Formats:
  vt        VT byte stream of the recorded session's final visible
            state (replay through libghostty's vt formatter).
            Default when stdout is a tty — `+"`hoot log abc`"+` shows the
            screen the way `+"`cat`"+`-ing a saved snapshot would.
  plain     Plain-text content of the final visible state. Strips
            SGR/cursor escapes; emits codepoints + newlines. Default
            when stdout is a pipe (so `+"`hoot log abc | grep error`"+`
            works naturally on visible text).
  asciinema asciicast v2 JSONL transcoded from the binary log.
            Pipe to `+"`asciinema play -`"+` to replay at recorded timing.

Flags:
  --format <fmt>     vt | plain | asciinema (default: vt on tty, plain on pipe)
  --state-dir <d>    session state directory (default: ~/.hoot)
  --strict           exact id-prefix match only — no fzf, no picker
  --all              include exited (dead) sessions in id-prefix and picker resolution

Common usage:
  hoot log abc                              # screen render of the recording
  hoot log abc | less                       # paged plain text
  hoot log abc | grep -i error              # greppable plain text
  hoot log abc --format asciinema | asciinema play -
  hoot log                                  # interactive picker (live only)
  hoot log --all                            # picker over live + exited sessions
`)
	}
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	formatFlag := fs.String("format", "", "output format: vt|plain|asciinema (default vt on tty, plain on pipe)")
	strict := fs.Bool("strict", false, "exact id-prefix match only — no fzf, no picker")
	all := fs.Bool("all", false, "include exited (dead) sessions in id-prefix and picker resolution")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2, err: err}
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return &exitError{code: 2, err: fmt.Errorf("expected at most one <id-or-prefix> argument")}
	}
	var target string
	if len(rest) == 1 {
		target = rest[0]
	}

	format, err := resolveLogFormat(*formatFlag)
	if err != nil {
		return &exitError{code: 2, err: err}
	}

	key, code, err := resolveSessionKey(target, *strict, nil, *stateDir, pickerOptions{Verb: "log", AliveOnly: !*all})
	if err != nil {
		return &exitError{code: code, err: err}
	}

	logPath := filepath.Join(*stateDir, key, "pty.hootty.log")
	statePath := filepath.Join(*stateDir, key, "state.json")

	switch format {
	case "asciinema":
		return runLogAsciinema(logPath, statePath, os.Stdout)
	case "vt", "plain":
		rfmt := session.ReplayFormatVT
		if format == "plain" {
			rfmt = session.ReplayFormatPlain
		}
		return runLogReplay(logPath, rfmt, os.Stdout)
	default:
		return &exitError{code: 2, err: fmt.Errorf("unknown --format %q", format)}
	}
}

// resolveLogFormat picks the output format. Empty flag means
// "default" — vt on a tty, plain on a pipe. Explicit values are
// validated.
func resolveLogFormat(flag string) (string, error) {
	switch flag {
	case "":
		if stdoutIsTTY() {
			return "vt", nil
		}
		return "plain", nil
	case "vt", "plain", "asciinema":
		return flag, nil
	default:
		return "", fmt.Errorf("invalid --format %q (want vt|plain|asciinema)", flag)
	}
}

// runLogReplay walks the binary recording into a fresh libghostty
// Terminal, then snapshots in the requested format.
func runLogReplay(logPath string, format session.ReplayFormat, out io.Writer) error {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &exitError{code: 1, err: fmt.Errorf("recording not found: %s", logPath)}
		}
		return err
	}
	defer f.Close()

	dec := session.NewFrameDecoder(f)
	first, err := dec.Next()
	if err != nil {
		return fmt.Errorf("read prelude: %w", err)
	}
	if first.Type != session.FrameTypeResize || len(first.Payload) != 4 {
		return fmt.Errorf("recording's first frame is not a resize")
	}
	cols := uint16(first.Payload[0]) | uint16(first.Payload[1])<<8
	rows := uint16(first.Payload[2]) | uint16(first.Payload[3])<<8

	r, err := session.NewLibghosttyReplay(cols, rows)
	if err != nil {
		return err
	}
	defer r.Close()

	for {
		fr, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		switch fr.Type {
		case session.FrameTypeOutput:
			r.VTWrite(fr.Payload)
		case session.FrameTypeResize:
			c := uint16(fr.Payload[0]) | uint16(fr.Payload[1])<<8
			rw := uint16(fr.Payload[2]) | uint16(fr.Payload[3])<<8
			if err := r.Resize(c, rw); err != nil {
				return fmt.Errorf("resize: %w", err)
			}
		}
	}
	snap, err := r.Snapshot(format)
	if err != nil {
		return err
	}
	if _, err := out.Write(snap); err != nil {
		return err
	}
	// vt/plain snapshots already include trailing newlines where the
	// formatter wants them; don't pad an extra one.
	return nil
}

// runLogAsciinema stream-transcodes the binary recording into
// asciicast v2 JSONL on out, with header metadata pulled from
// state.json (best-effort — missing file is non-fatal).
func runLogAsciinema(logPath, statePath string, out io.Writer) error {
	st, err := session.LoadStateFile(statePath)
	if err != nil {
		// state.json read errors are non-fatal — degrade to a
		// header without metadata.
		st = nil
	}
	header := session.AsciicastHeaderFromState(st)

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &exitError{code: 1, err: fmt.Errorf("recording not found: %s", logPath)}
		}
		return err
	}
	defer f.Close()

	return session.WriteAsciicastJSONL(f, out, header)
}
