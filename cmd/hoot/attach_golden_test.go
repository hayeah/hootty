package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/attachetest"
)

func TestAttachGoldenSnapshots(t *testing.T) {
	t.Run("empty remote scrollback fresh visible screen", func(t *testing.T) {
		remote := attachetest.NewRemote(t, 40, 8)
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, []byte("\x1b[Hfresh-visible\r\n\x1b[32mready\x1b[0m"), "ready")

		finish := startAttach(t, remote, local, 40, 8, "empty")
		defer finish()
		local.WaitText(t, "fresh-visible")

		attachetest.CompareGolden(t, "attach-empty-scrollback", local.Snapshot(t))
	})

	t.Run("substantial remote scrollback short visible screen", func(t *testing.T) {
		remote := attachetest.NewRemote(t, 40, 8)
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, substantialScrollbackFixture(), "short-visible")

		finish := startAttach(t, remote, local, 40, 8, "scrollback")
		defer finish()
		local.WaitText(t, "short-visible")

		attachetest.CompareGolden(t, "attach-substantial-scrollback", local.Snapshot(t))
	})

	t.Run("full visible screen plus scrollback", func(t *testing.T) {
		remote := attachetest.NewRemote(t, 40, 8)
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, fullScreenFixture(40, 8), "FULL-08")

		finish := startAttach(t, remote, local, 40, 8, "full")
		defer finish()
		local.WaitText(t, "FULL-08")

		attachetest.CompareGolden(t, "attach-full-visible-screen", local.Snapshot(t))
	})

	t.Run("asciicast playback follows visible screen phase", func(t *testing.T) {
		rec, err := hootty.NewRecorder(filepath.Join(t.TempDir(), "pty.cast"), 40, 8)
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		remote := attachetest.NewRemoteWithOptions(t, 40, 8, hootty.WithRecorder(rec))
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, []byte("playback-a\r\nplayback-b\r\n\x1b[H\x1b[2Jscreen-now"), "screen-now")

		finish := startAttachWithPlayback(t, remote, local, 40, 8, "playback", attachPlaybackConfig{
			Enabled: true,
			Window:  time.Minute,
			Speed:   1000,
		})
		defer finish()
		local.WaitRawText(t, "playback-b")

		attachetest.CompareGolden(t, "attach-asciicast-playback-raw", local.RawSnapshot(t))
	})

	t.Run("detach clears viewport and keeps scrollback", func(t *testing.T) {
		remote := attachetest.NewRemote(t, 40, 8)
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, substantialScrollbackFixture(), "short-visible")

		finish := startAttach(t, remote, local, 40, 8, "detach")
		local.WaitText(t, "short-visible")
		remote.WriteAndWait(t, []byte("\r\nlive-after-attach\r\n"), "live-after-attach")
		local.WaitText(t, "live-after-attach")
		finish()
		local.WaitText(t, "[disconnected. detach @ local]")

		attachetest.CompareGolden(t, "detach-clears-viewport", local.Snapshot(t))
	})

	t.Run("reattach boundaries stay scannable", func(t *testing.T) {
		remote := attachetest.NewRemote(t, 40, 8)
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, substantialScrollbackFixture(), "short-visible")

		first := startAttach(t, remote, local, 40, 8, "reattach")
		local.WaitText(t, "short-visible")
		remote.WriteAndWait(t, []byte("\r\nbetween-attach-live\r\n"), "between-attach-live")
		local.WaitText(t, "between-attach-live")
		first()
		local.WaitText(t, "[disconnected. reattach @ local]")

		second := startAttach(t, remote, local, 40, 8, "reattach")
		defer second()
		local.WaitText(t, "between-attach-live")

		attachetest.CompareGolden(t, "reattach-boundaries", local.Snapshot(t))
	})

	t.Run("resize between detach and reattach", func(t *testing.T) {
		remote := attachetest.NewRemote(t, 40, 8)
		local := attachetest.NewLocalTerminal(t, 40, 8)
		remote.WriteAndWait(t, fullScreenFixture(40, 8), "FULL-08")

		first := startAttach(t, remote, local, 40, 8, "resize")
		local.WaitText(t, "FULL-08")
		first()

		local.Resize(t, 40, 5)
		remote.WriteAndWait(t, fullScreenFixture(40, 5), "FULL-05")
		shrink := startAttach(t, remote, local, 40, 5, "resize")
		local.WaitText(t, "FULL-05")
		shrink()

		local.Resize(t, 40, 10)
		remote.WriteAndWait(t, fullScreenFixture(40, 10), "FULL-10")
		grow := startAttach(t, remote, local, 40, 10, "resize")
		defer grow()
		local.WaitText(t, "FULL-10")

		attachetest.CompareGolden(t, "resize-reattach", local.Snapshot(t))
	})
}

func startAttach(t *testing.T, remote *attachetest.Remote, local *attachetest.LocalTerminal, cols, rows uint16, session string) func() {
	t.Helper()
	return startAttachWithPlayback(t, remote, local, cols, rows, session, attachPlaybackConfig{
		Enabled: true,
		Window:  5 * time.Minute,
		Speed:   8,
	})
}

func startAttachWithPlayback(t *testing.T, remote *attachetest.Remote, local *attachetest.LocalTerminal, cols, rows uint16, session string, playback attachPlaybackConfig) func() {
	t.Helper()
	conn := remote.DialAttach(t)
	if err := upgradeAttachConn(conn, remote.AttachURL()); err != nil {
		t.Fatalf("upgrade attach: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	shared := &sharedSession{wake: make(chan struct{}, 1)}
	var attached atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- runSession(ctx, conn, cols, rows, shared, attachLabel{Session: session, Host: "local"}, &attached, attachWriters{
			stdout: local,
			stderr: io.Discard,
		}, playback)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !attached.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for attach session to start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var once atomic.Bool
	return func() {
		if once.Swap(true) {
			return
		}
		cancel()
		_ = conn.Close()
		select {
		case err := <-done:
			if err != nil && !isClosedConn(err) {
				t.Fatalf("attach session ended with error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for attach session to end")
		}
		if attached.Load() {
			emitDetach(local, attachLabel{Session: session, Host: "local"})
		}
	}
}

func isClosedConn(err error) bool {
	if err == nil {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "closed pipe") ||
		err == net.ErrClosed
}

func substantialScrollbackFixture() []byte {
	var b bytes.Buffer
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "history-%02d\r\n", i)
	}
	b.WriteString("\x1b[H\x1b[2J")
	b.WriteString("short-visible\r\n")
	b.WriteString("\x1b[35mstyled-ready\x1b[0m")
	return b.Bytes()
}

func fullScreenFixture(cols, rows int) []byte {
	var b bytes.Buffer
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "prelude-%02d\r\n", i)
	}
	b.WriteString("\x1b[H\x1b[2J")
	for row := 1; row <= rows; row++ {
		line := fmt.Sprintf("FULL-%02d ", row)
		for len(line) < cols {
			line += fmt.Sprintf("%d", row%10)
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s", row, line[:cols])
	}
	b.WriteString("\x1b[2;7H\x1b[1mCUR\x1b[0m")
	return b.Bytes()
}
