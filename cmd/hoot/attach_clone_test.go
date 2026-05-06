package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

func TestRunAttachLoopPrefixCClonesAndSwitches(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	defer stdinR.Close()
	defer stdinW.Close()
	defer stdoutR.Close()
	defer stdoutW.Close()
	defer stderrR.Close()
	defer stderrW.Close()

	oldStdin, oldStdout, oldStderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdinR, stdoutW, stderrW
	defer func() {
		os.Stdin, os.Stdout, os.Stderr = oldStdin, oldStdout, oldStderr
	}()
	go io.Copy(io.Discard, stdoutR)
	go io.Copy(io.Discard, stderrR)

	firstConnected := make(chan struct{})
	secondConnected := make(chan struct{})
	var cloneCalls atomic.Int32
	second := fakeAttachTargetForCloneTest("clone", secondConnected)
	first := fakeAttachTargetForCloneTest("parent", firstConnected)
	first.clone = func(context.Context) (attachTarget, error) {
		cloneCalls.Add(1)
		return second, nil
	}

	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := runAttachLoop(first, 0x1e, false, attachPlaybackConfig{})
		done <- struct {
			code int
			err  error
		}{code: code, err: err}
	}()

	select {
	case <-firstConnected:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first attach")
	}
	if _, err := stdinW.Write([]byte{0x1e, 'c'}); err != nil {
		t.Fatalf("write clone prefix: %v", err)
	}
	select {
	case <-secondConnected:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cloned attach")
	}
	if cloneCalls.Load() != 1 {
		t.Fatalf("clone calls = %d, want 1", cloneCalls.Load())
	}
	if _, err := stdinW.Write([]byte{0x1e, '.'}); err != nil {
		t.Fatalf("write detach prefix: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil || result.code != 0 {
			t.Fatalf("runAttachLoop = code %d err %v, want clean exit", result.code, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for attach loop exit")
	}
}

func fakeAttachTargetForCloneTest(name string, connected chan<- struct{}) attachTarget {
	return attachTarget{
		label: attachLabel{Session: name, Host: "test"},
		dial: func(context.Context) (net.Conn, error) {
			client, server := net.Pipe()
			go runFakeAttachServerForCloneTest(server, name, connected)
			return client, nil
		},
	}
}

func runFakeAttachServerForCloneTest(conn net.Conn, name string, connected chan<- struct{}) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	typ, _, err := attachwire.ReadFrame(r)
	if err != nil || typ != attachwire.MsgHello {
		return
	}
	close(connected)
	_ = attachwire.WriteFrame(conn, attachwire.MsgSnapshotScreen, []byte(name))
	for {
		typ, _, err := attachwire.ReadFrame(r)
		if err != nil {
			return
		}
		if typ == attachwire.MsgPing {
			_ = attachwire.WriteFrame(conn, attachwire.MsgPong, nil)
		}
	}
}
