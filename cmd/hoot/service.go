package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hayeah/hootty"
)

// RunCmdService is the reference Service: spawn one child, publish
// state transitions (starting → running → exited), return when the
// child exits. No restart, no briefing, no idle detection.
type RunCmdService struct {
	Cmd      string
	Args     []string
	StateDir string
	Key      string

	// child holds the live *exec.Cmd between Start and Wait so the
	// /signal handler can deliver a signal to the supervised PID
	// when the PTY's tcgetpgrp lookup fails. nil before Start and
	// after Wait returns.
	child atomic.Pointer[exec.Cmd]

	// session is captured at Run entry so handleSignal can reach
	// PTY().SignalForeground without going through a closure.
	session atomic.Pointer[sessionRef]
}

// sessionRef is a tiny wrapper so atomic.Pointer can hold an
// interface (atomic.Pointer requires a concrete pointer type).
type sessionRef struct{ s session.Session }

type runState struct {
	State     string `json:"state"`
	Cmd       string `json:"cmd"`
	PID       int    `json:"pid,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	ExitedAt  string `json:"exited_at,omitempty"`
}

func (s *RunCmdService) cmdString() string {
	parts := append([]string{s.Cmd}, s.Args...)
	return strings.Join(parts, " ")
}

// Run implements session.Service. Stdin/Stdout/Stderr are
// inherited from the session — already on the PTY slave. Setctty
// on the child is what lets master-side TIOCSWINSZ keep working
// after the child's job-control configures the fg pgrp; without
// it macOS surfaces EIO on the master ioctl as soon as the shell
// runs tcsetpgrp.
func (s *RunCmdService) Run(ctx context.Context, super session.Session) error {
	if s.Cmd == "" {
		return errors.New("RunCmdService: Cmd is required")
	}
	s.session.Store(&sessionRef{s: super})
	defer s.session.Store(nil)
	super.Mux().HandleFunc("/signal", s.handleSignal)
	if s.StateDir != "" && s.Key != "" {
		super.Mux().HandleFunc("/clone", s.handleClone)
	}

	started := time.Now().UTC().Format(time.RFC3339)
	state := runState{
		State:     "starting",
		Cmd:       s.cmdString(),
		StartedAt: started,
	}
	if err := super.UpdateState(state); err != nil {
		return fmt.Errorf("publish starting: %w", err)
	}

	cmd := exec.CommandContext(ctx, s.Cmd, s.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = sanitizeChildEnv(os.Environ(), s.Key)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	s.child.Store(cmd)
	defer s.child.Store(nil)

	if err := cmd.Start(); err != nil {
		state.State = "exited"
		state.ExitCode = -1
		state.ExitedAt = time.Now().UTC().Format(time.RFC3339)
		_ = super.UpdateState(state)
		return fmt.Errorf("start %q: %w", s.Cmd, err)
	}

	state.State = "running"
	state.PID = cmd.Process.Pid
	if err := super.UpdateState(state); err != nil {
		return fmt.Errorf("publish running: %w", err)
	}

	waitErr := cmd.Wait()

	state.State = "exited"
	state.ExitedAt = time.Now().UTC().Format(time.RFC3339)
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			state.ExitCode = exitErr.ExitCode()
		} else {
			state.ExitCode = -1
		}
	}
	_ = super.UpdateState(state)
	return nil
}

// signalRequest is the JSON body of POST /signal: {"signal": "TERM"}.
// The "signal" field accepts a name (with or without SIG prefix,
// case-insensitive) or a small positive integer.
type signalRequest struct {
	Signal string `json:"signal"`
}

// handleSignal routes a POST /signal RPC into a foreground-pgrp
// kill(2). Falls back to signaling the supervised child PID if the
// PTY reports no foreground process group (ErrNoForeground).
//
// Status codes:
//
//	204 — signal delivered
//	400 — bad body / unparseable signal name
//	405 — non-POST
//	409 — child not running yet (or already exited) and no fg pgrp
//	500 — kill(2) failed for any other reason
func (s *RunCmdService) handleSignal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body signalRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	name := body.Signal
	if name == "" {
		name = "TERM"
	}
	sig, err := parseSignal(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Preferred path: deliver to the PTY's current foreground pgrp.
	if ref := s.session.Load(); ref != nil {
		if pty := ref.s.PTY(); pty != nil {
			err := pty.SignalForeground(sig)
			if err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if !errors.Is(err, session.ErrNoForeground) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// fall through to direct-PID fallback
		}
	}

	// Fallback: signal the supervised child directly.
	cmd := s.child.Load()
	if cmd == nil || cmd.Process == nil {
		http.Error(w, "child not running", http.StatusConflict)
		return
	}
	if err := cmd.Process.Signal(sig); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			http.Error(w, "child already exited", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *RunCmdService) handleClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body cloneSessionReq
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	resp, err := cloneSession(session.NewStore(s.StateDir), s.StateDir, s.Key, body.Key, body.Env)
	if err != nil {
		writeCloneError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// sanitizeChildEnv strips TMUX/TMUX_PANE and forces TERM=xterm-
// 256color so the child doesn't think it's running inside an
// outer multiplexer / different emulator. (See dotfiles' ptydemo
// for the original motivation: powerlevel10k probes $TMUX and
// emits tmux-native title escapes that libghostty doesn't parse.)
//
// sessionKey, if non-empty, is published as $HOOT_SESSION so nested
// hoot invocations from inside this child can detect they're already
// in a hoot session and refuse to take over the terminal a second
// time. Mirrors tmux's $TMUX convention.
func sanitizeChildEnv(parent []string, sessionKey string) []string {
	const childTerm = "xterm-256color"
	// TMUX / TMUX_PANE: powerlevel10k checks these to decide it's
	// nested inside tmux and emits tmux-private title escapes
	// (ESC-k ... ESC-\) that libghostty doesn't parse, so they render
	// as literal text. Strip them so p10k stays in xterm mode.
	// TERM: dropped so we can force xterm-256color below; otherwise
	// a parent TERM=tmux-256color would have the same effect on p10k.
	// HOOT_SESSION: dropped here so the inner-most session's key wins
	// if hoot ever ends up nested anyway (override via unset).
	drop := map[string]bool{
		"TMUX":         true,
		"TMUX_PANE":    true,
		"TERM":         true,
		hootSessionEnv: true,
	}
	out := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			out = append(out, kv)
			continue
		}
		if drop[kv[:eq]] {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "TERM="+childTerm)
	if sessionKey != "" {
		out = append(out, hootSessionEnv+"="+sessionKey)
	}
	return out
}
