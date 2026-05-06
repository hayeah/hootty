package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
}

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
	cmd.Env = sanitizeChildEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

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

// sanitizeChildEnv strips TMUX/TMUX_PANE and forces TERM=xterm-
// 256color so the child doesn't think it's running inside an
// outer multiplexer / different emulator. (See dotfiles' ptydemo
// for the original motivation: powerlevel10k probes $TMUX and
// emits tmux-native title escapes that libghostty doesn't parse.)
func sanitizeChildEnv(parent []string) []string {
	const childTerm = "xterm-256color"
	drop := map[string]bool{
		"TMUX":      true,
		"TMUX_PANE": true,
		"TERM":      true,
	}
	out := make([]string, 0, len(parent)+1)
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
	return out
}
