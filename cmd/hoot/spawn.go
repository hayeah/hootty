package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type spawnSpec struct {
	Argv         []string
	CWD          string
	EnvOverrides map[string]string
	Cols         uint16
	Rows         uint16
}

func (s spawnSpec) normalized() spawnSpec {
	if s.Cols == 0 {
		s.Cols = 80
	}
	if s.Rows == 0 {
		s.Rows = 24
	}
	return s
}

// spawnSessionFromSpec forks `hoot __session --state-dir <d>
// --key <k> --cwd <cwd> -- <argv...>` as a detached child and
// waits for rpc.sock to appear.
func spawnSessionFromSpec(stateDir, key string, spec spawnSpec) (string, error) {
	spec = spec.normalized()
	if len(spec.Argv) == 0 {
		return "", fmt.Errorf("missing argv")
	}
	if spec.CWD == "" {
		return "", fmt.Errorf("missing cwd")
	}
	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("abs state-dir: %w", err)
	}

	master, slave, err := pty.Open()
	if err != nil {
		return "", fmt.Errorf("pty.Open: %w", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Cols: spec.Cols, Rows: spec.Rows}); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return "", fmt.Errorf("setsize: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	hootArgs := []string{
		"__session",
		"--state-dir", absStateDir,
		"--key", key,
		"--cwd", spec.CWD,
		"--",
	}
	hootArgs = append(hootArgs, spec.Argv...)

	cmd := exec.Command(self, hootArgs...)
	cmd.Dir = spec.CWD
	cmd.Env = mergeEnv(os.Environ(), spec.EnvOverrides)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.ExtraFiles = []*os.File{master}
	// Session runs in its own session (Setsid). The Service
	// inside will Setctty to claim the slave; keeping the
	// session session-less for this tty is what lets master-side
	// TIOCSWINSZ keep working after the child takes the fg pgrp.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return "", fmt.Errorf("fork hoot: %w", err)
	}

	// Parent is done with both ends; the child has its own dups.
	_ = slave.Close()
	_ = master.Close()
	_ = cmd.Process.Release()

	sockPath := filepath.Join(absStateDir, key, "rpc.sock")
	if err := waitForSocket(sockPath, 3*time.Second); err != nil {
		return "", err
	}
	return sockPath, nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, kv := range base {
		name, _, ok := strings.Cut(kv, "=")
		if ok && overrides[name] != "" {
			out = append(out, name+"="+overrides[name])
			seen[name] = true
			continue
		}
		if ok {
			if _, replace := overrides[name]; replace {
				out = append(out, name+"="+overrides[name])
				seen[name] = true
				continue
			}
		}
		out = append(out, kv)
	}
	for name, value := range overrides {
		if !seen[name] {
			out = append(out, name+"="+value)
		}
	}
	return out
}

// waitForSocket polls for the unix socket file to appear.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}
