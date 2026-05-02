package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"

	"github.com/hayeah/supervisor"
	"github.com/hayeah/supervisor/internal/attachwire"
)

// cmdAttach implements `supervise attach`. It dials rpc.sock,
// upgrades the connection into the attach binary protocol, puts the
// local terminal into raw mode, and ferries bytes between local
// stdio and the supervisor's PTY master.
//
// Exit codes (per spec):
//
//	0   clean detach (server EOF after normal child exit, or
//	    user-initiated detach via <prefix>d)
//	1   protocol error, dial failure, bad state dir
//	2   argument error (no session matched, ambiguous prefix,
//	    bad --prefix-key)
//	130 terminated by signal we trapped (SIGINT)
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: supervise attach [flags] <id-or-prefix>

  --state-dir <dir>      session state directory (default: ~/.supervise)
  --no-full-replay       send a libghostty snapshot instead of streaming pty.log
  --prefix-key <key>     command prefix byte (default: C-b). Forms: C-b, ^b,
                         0x02, or a single ASCII control byte.

Inside an attach: <prefix>d detaches; <prefix><prefix> sends a literal
prefix byte to the remote; <prefix>? prints help.
`)
	}
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	noFullReplay := fs.Bool("no-full-replay", false, "send a libghostty snapshot instead of streaming pty.log")
	prefixSpec := fs.String("prefix-key", "C-b", "command prefix byte (e.g. C-b, ^a, 0x1c)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return 2
	}

	prefixByte, err := attachwire.ParsePrefixKey(*prefixSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervise attach: %v\n", err)
		return 2
	}

	store := supervisor.NewStore(*stateDir)
	state, err := store.Resolve(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervise attach: %v\n", err)
		return 2
	}

	sockPath := filepath.Join(*stateDir, state.Supervisor.Key, "rpc.sock")
	replayMode := "full"
	if *noFullReplay {
		replayMode = "snapshot"
	}

	exitCode, err := runAttach(sockPath, prefixByte, replayMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervise attach: %v\n", err)
	}
	return exitCode
}

// runAttach is the meat of `supervise attach`. Returns (exitCode,
// err). On clean detach exitCode=0 err=nil. On signal trap
// exitCode=130. On dial / protocol error exitCode=1.
func runAttach(sockPath string, prefixByte byte, replayMode string) (int, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		// Long-path fallback: bind a relative path by chdir'ing into
		// the state dir, same trick the server uses.
		if relConn, relErr := dialUnixRelative(sockPath); relErr == nil {
			conn = relConn
			err = nil
		}
		if err != nil {
			return 1, fmt.Errorf("dial %s: %w", sockPath, err)
		}
	}
	defer conn.Close()

	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	// Send the HTTP/1.1 Upgrade request.
	req, err := http.NewRequest(http.MethodGet, "http://supervise/attach", nil)
	if err != nil {
		return 1, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Upgrade", "supervise-attach/1")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(bufrw); err != nil {
		return 1, fmt.Errorf("write upgrade: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		return 1, fmt.Errorf("flush upgrade: %w", err)
	}

	// Read the response. Anything other than 101 is a setup error;
	// print body to stderr and return code 1.
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		return 1, fmt.Errorf("read upgrade response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return 1, fmt.Errorf("server refused upgrade: %s\n%s", resp.Status, string(body))
	}
	resp.Body.Close()

	// Determine local size. Default to 80x24 if stdin isn't a tty
	// (which is fine for scripted/expect-driven attaches).
	cols, rows := uint16(80), uint16(24)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		c, r, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil && c > 0 && r > 0 {
			cols, rows = uint16(c), uint16(r)
		}
	}

	// Raw mode if stdin is a tty. Top-level defer Restore so a panic
	// at any depth still bubbles through.
	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return 1, fmt.Errorf("term.MakeRaw: %w", err)
		}
		defer func() {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
			// Belt-and-suspenders for a child that left altscreen on
			// or hid the cursor or set weird SGR.
			_, _ = os.Stderr.Write([]byte("\x1b[?1049l\x1b[?25h\x1b[0m"))
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal trap. In raw mode Ctrl-C is just a byte forwarded to the
	// remote, so SIGINT here is from `kill -INT` not the keyboard.
	// SIGTERM/SIGHUP come from pkill / parent dying / terminal close.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	signaled := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			close(signaled)
			cancel()
		case <-ctx.Done():
		}
	}()

	// Single-writer goroutine for outbound frames so Hello / Input /
	// Size never interleave bytes mid-frame.
	type outMsg struct {
		typ     byte
		payload []byte
	}
	outCh := make(chan outMsg, 256)
	writerDone := make(chan error, 1)
	go func() {
		var werr error
		for m := range outCh {
			if err := attachwire.WriteFrame(conn, m.typ, m.payload); err != nil {
				werr = err
				break
			}
		}
		writerDone <- werr
	}()
	send := func(typ byte, payload []byte) bool {
		select {
		case outCh <- outMsg{typ, payload}:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Send Hello.
	helloPayload, _ := json.Marshal(attachwire.Hello{
		Cols:       cols,
		Rows:       rows,
		ReplayMode: replayMode,
	})
	if !send(attachwire.MsgHello, helloPayload) {
		return 1, errors.New("ctx cancelled before Hello")
	}

	// SIGWINCH: re-read local size, send Size{cols,rows}. Coalesce —
	// if a second SIGWINCH arrives while we're still preparing the
	// first, we just send the latest size.
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)
	go func() {
		for {
			select {
			case <-winchCh:
				// Drain any backlog so we coalesce.
				for {
					select {
					case <-winchCh:
					default:
						goto sendIt
					}
				}
			sendIt:
				c, r, err := term.GetSize(int(os.Stdin.Fd()))
				if err != nil || c == 0 || r == 0 {
					continue
				}
				payload, _ := json.Marshal(attachwire.Size{Cols: uint16(c), Rows: uint16(r)})
				if !send(attachwire.MsgSize, payload) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// stdin → server, with prefix-key state machine. We do NOT
	// terminate the attach on stdin EOF (the user may have piped
	// finite input but still want to watch live output). Detach is
	// server EOF / ctx cancel / <prefix>d only.
	stdinDone := make(chan error, 1)
	go func() {
		err := runStdinLoop(ctx, prefixByte, send, cancel)
		stdinDone <- err
	}()

	// server → stdout. This goroutine reads framed messages and
	// dispatches Output/Size. Returns when the server closes the
	// connection (clean detach: server EOF) or hits a protocol
	// error.
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runServerLoop(ctx, bufrw.Reader)
	}()

	// Wait for any of: server EOF, ctx cancel (signal trap or
	// <prefix>d from stdin loop). Stdin EOF alone does not detach.
	var loopErr error
	select {
	case err := <-serverDone:
		loopErr = err
	case <-ctx.Done():
	}

	// Tear down the conn so the other goroutines unblock.
	cancel()
	_ = conn.Close()
	close(outCh)
	<-writerDone

	// Drain remaining goroutines best-effort. They'll bail on closed
	// conn / ctx.
	select {
	case <-serverDone:
	default:
	}
	select {
	case <-stdinDone:
	default:
	}

	// Determine exit code.
	select {
	case <-signaled:
		return 130, nil
	default:
	}
	if loopErr != nil && !errors.Is(loopErr, io.EOF) && !errors.Is(loopErr, net.ErrClosed) {
		return 1, loopErr
	}
	return 0, nil
}

// runStdinLoop reads stdin one byte at a time and runs the prefix-key
// state machine. Returns when stdin EOFs, ctx cancels, or the user
// types <prefix>d (cancel is invoked then, ctx fires next iteration).
func runStdinLoop(ctx context.Context, prefix byte, send func(byte, []byte) bool, cancel context.CancelFunc) error {
	type readResult struct {
		b   byte
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				readCh <- readResult{buf[0], nil}
			}
			if err != nil {
				readCh <- readResult{0, err}
				return
			}
		}
	}()

	const (
		stateNormal     = 0
		stateAfterPref  = 1
	)
	state := stateNormal
	for {
		select {
		case <-ctx.Done():
			return nil
		case rr := <-readCh:
			if rr.err != nil {
				return rr.err
			}
			b := rr.b
			switch state {
			case stateNormal:
				if b == prefix {
					state = stateAfterPref
					continue
				}
				if !send(attachwire.MsgInput, []byte{b}) {
					return nil
				}
			case stateAfterPref:
				switch b {
				case prefix:
					if !send(attachwire.MsgInput, []byte{prefix}) {
						return nil
					}
				case 'd':
					cancel()
					return nil
				case '?':
					fmt.Fprintln(os.Stderr,
						"\r\nsupervise attach: <prefix>d=detach, <prefix><prefix>=literal, <prefix>?=help\r")
				default:
					// silent ignore (tmux beeps; we don't)
				}
				state = stateNormal
			}
		}
	}
}

// runServerLoop reads frames from the server and dispatches them.
// Output → stdout. Size → stderr status line. Returns on EOF or
// protocol error.
func runServerLoop(ctx context.Context, r *bufio.Reader) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		typ, payload, err := attachwire.ReadFrame(r)
		if err != nil {
			return err
		}
		switch typ {
		case attachwire.MsgOutput:
			if _, werr := os.Stdout.Write(payload); werr != nil {
				return werr
			}
		case attachwire.MsgSize:
			var sz attachwire.Size
			if err := json.Unmarshal(payload, &sz); err == nil {
				lc, lr := localSize()
				if lc != 0 && (sz.Cols != lc || sz.Rows != lr) {
					fmt.Fprintf(os.Stderr,
						"\r\n\x1b[2m[supervise: remote pty %d×%d, your terminal %d×%d]\x1b[0m\r\n",
						sz.Cols, sz.Rows, lc, lr)
				}
			}
		case attachwire.MsgHello, attachwire.MsgInput:
			return fmt.Errorf("server sent unexpected frame type 0x%02x", typ)
		default:
			return fmt.Errorf("server sent unknown frame type 0x%02x", typ)
		}
	}
}

func localSize() (uint16, uint16) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return 0, 0
	}
	c, r, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return 0, 0
	}
	return uint16(c), uint16(r)
}

// dialUnixRelative chdirs into the state dir to bind a short relative
// socket path; matches the server-side fallback in supervisor.go.
func dialUnixRelative(absPath string) (net.Conn, error) {
	dir := filepath.Dir(absPath)
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(dir); err != nil {
		return nil, err
	}
	defer os.Chdir(cwd)
	return net.Dial("unix", filepath.Base(absPath))
}

