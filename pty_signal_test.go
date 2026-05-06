package session

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// TestSignalForegroundDeliversToFGPgrp exercises the kill(2) call
// at the heart of `hoot kill`: with a child that claimed the PTY
// slave as its controlling terminal (Setsid+Setctty), the kernel
// records the child's pgid as the master's fg pgrp; calling
// SignalForeground(SIGTERM) must hit the child and make Wait return
// with status=signal:terminated.
func TestSignalForegroundDeliversToFGPgrp(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()

	p, err := NewLibghosttyPTY(master, 80, 24)
	if err != nil {
		_ = slave.Close()
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	defer p.Close()

	// `sleep 30` is a child we can signal cleanly. Setsid+Setctty
	// + Ctty=0 mirror what cmd/hoot/service.go does for the real
	// monitored child.
	cmd := exec.Command("sleep", "30")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		t.Fatalf("start sleep: %v", err)
	}
	// Parent doesn't need the slave fd; the child has it as ctty.
	_ = slave.Close()
	defer func() {
		// Belt-and-braces in case the test bails early.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// The kernel needs a moment to set the fg pgrp on the slave.
	// Poll tcgetpgrp until it returns the child's pgid.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if waited := pollFgPgrpReady(p, cmd.Process.Pid); waited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for fg pgrp = child pgid")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := p.SignalForeground(syscall.SIGTERM); err != nil {
		t.Fatalf("SignalForeground(TERM): %v", err)
	}

	// Wait must return; the child died from TERM.
	waitErr := cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		t.Fatalf("wait err = %v, want ExitError", waitErr)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("Sys() = %T, want syscall.WaitStatus", ee.Sys())
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("wait status = %v signal=%v signaled=%v, want SIGTERM",
			ws, ws.Signal(), ws.Signaled())
	}
}

// pollFgPgrpReady returns true once tcgetpgrp on the master matches
// the child's pgid (which under Setsid equals child PID).
func pollFgPgrpReady(p *LibghosttyPTY, childPid int) bool {
	pgid, err := unix.IoctlGetInt(int(p.master.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return false
	}
	return pgid == childPid
}
