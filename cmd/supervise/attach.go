package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/hayeah/supervisor"
	"github.com/hayeah/supervisor/internal/attachwire"
)

// cmdAttach implements `supervise attach`. Two transports:
//
//   - Local (default): dials <state-dir>/<key>/rpc.sock and performs
//     the supervise-attach/1 HTTP/1.1 Upgrade.
//   - Remote (`--host`): dials a `supervise serve` host over TCP (or
//     TLS if the URL scheme is https://) and performs the same
//     Upgrade against /sessions/<id>/attach-raw. Server resolves the
//     short-id; the client passes its raw user-typed argument.
//
// The remote path supports automatic reconnect (default; `--no-reconnect`
// to opt out). Termios stays raw across drops; on reconnect the client
// sends a fresh Hello with current local cols/rows, and the server
// repaints by sending a libghostty snapshot of the current screen.
//
// Exit codes:
//
//	0   clean detach (server EOF after normal child exit, <prefix>.,
//	    or remote drop with --no-reconnect)
//	1   protocol error, dial failure on first connect, bad state dir
//	2   argument error (no session matched, ambiguous prefix, bad
//	    --prefix-key, --host parse error)
//	130 terminated by a trapped signal (SIGINT/SIGTERM/SIGHUP)
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: supervise attach [flags] <id-or-prefix>

  --host <addr>          remote `+"`supervise serve`"+` host: bare host:port,
                         http://host:port, or https://host:port
  --state-dir <dir>      session state directory (default: ~/.supervise);
                         only used for local attach (no --host)
  --no-reconnect         exit on first drop instead of auto-reconnecting
                         (--host only; ignored for local attach)
  --prefix-key <key>     command prefix byte (default: C-^). Forms: C-^, ^^,
                         0x1e, or a single ASCII control byte.

Inside an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte to the remote; <prefix>Ctrl-Z suspends supervise
attach (resume with fg); <prefix>? prints help.

During a remote disconnect: backoff is 1,2,4,8,16,30s capped at 30s and
retries forever. Press any key to wake the backoff and retry now.
<prefix>. still detaches cleanly.
`)
	}
	host := fs.String("host", "", "remote `supervise serve` host (host:port or URL)")
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	noReconnect := fs.Bool("no-reconnect", false, "exit on first drop instead of auto-reconnecting (--host only)")
	prefixSpec := fs.String("prefix-key", "C-^", "command prefix byte (e.g. C-^, ^a, 0x1c)")
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

	var dial dialFn
	var reconnect bool
	if *host != "" {
		base, err := parseHostFlag(*host)
		if err != nil {
			fmt.Fprintf(os.Stderr, "supervise attach: %v\n", err)
			return 2
		}
		dial = remoteDialer(base, rest[0])
		reconnect = !*noReconnect
	} else {
		store := supervisor.NewStore(*stateDir)
		state, err := store.Resolve(rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "supervise attach: %v\n", err)
			return 2
		}
		sockPath := filepath.Join(*stateDir, state.Supervisor.Key, "rpc.sock")
		dial = localDialer(sockPath)
	}

	exitCode, err := runAttachLoop(dial, prefixByte, reconnect)
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervise attach: %v\n", err)
	}
	return exitCode
}

// dialFn returns a connection that has already completed the
// supervise-attach/1 HTTP/1.1 Upgrade handshake — i.e. ready for
// attachwire frames in both directions.
type dialFn func(ctx context.Context) (net.Conn, error)

// runAttachLoop is the top-level driver. It owns termios, signal
// traps, the stdin reader and SIGWINCH watcher; everything below
// re-runs per attempt across reconnects.
//
// Returns the process exit code and an optional error to print.
func runAttachLoop(dial dialFn, prefixByte byte, reconnect bool) (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal trap. Raw-mode Ctrl-C is just a byte forwarded to the
	// remote, so SIGINT here is from `kill -INT` not the keyboard.
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

	// Termios raw if stdin is a tty. Set once; stays raw across
	// reconnects so we don't flicker between drops.
	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		var err error
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

	// Initial size; SIGWINCH updates it.
	cols, rows := localOrDefaultSize()
	var size atomic.Uint64
	size.Store(packSize(cols, rows))

	// Shared connection state. The stdin and SIGWINCH goroutines see
	// the *current* session's send func via this; if no session,
	// inputs are dropped and a wake is signaled.
	shared := &sharedSession{
		wake: make(chan struct{}, 1),
	}

	// SIGWINCH watcher: re-reads size, updates atomic, sends a Size
	// frame to the current session (if connected). Coalesces.
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
				c, r := localOrDefaultSize()
				size.Store(packSize(c, r))
				payload, _ := json.Marshal(attachwire.Size{Cols: c, Rows: r})
				shared.sendIfConnected(attachwire.MsgSize, payload)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Stdin reader + prefix-key FSM. Runs once across all reconnects.
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		runStdinFSM(ctx, prefixByte, shared, cancel)
	}()

	// Reconnect loop.
	loopErr := runConnectLoop(ctx, dial, reconnect, &size, shared)

	// Determine exit code from outer signals.
	select {
	case <-signaled:
		return 130, nil
	default:
	}
	if loopErr != nil {
		var hee *httpErrExit2
		if errors.As(loopErr, &hee) {
			fmt.Fprintln(os.Stderr, "supervise attach: "+hee.msg)
			return 2, nil
		}
		return 1, loopErr
	}
	return 0, nil
}

// runConnectLoop runs the connect→session→drop→reconnect FSM. It
// returns when:
//
//   - ctx is cancelled (user detach via <prefix>d, signal, etc.)
//   - the session ends cleanly (server EOF) → return nil
//   - reconnect is disabled and a drop or first-connect error occurs
//   - first connect fails (we always fail-fast on the first attempt
//     even with reconnect=true; a typo'd host should error in a beat)
func runConnectLoop(
	ctx context.Context,
	dial dialFn,
	reconnect bool,
	size *atomic.Uint64,
	shared *sharedSession,
) error {
	gotConnectedOnce := false
	for {
		// Dial.
		conn, err := dial(ctx)
		if err != nil {
			// User cancelled (e.g. <prefix>d before we connected, or
			// SIGINT during dial). Clean exit, not error.
			if ctx.Err() != nil {
				return nil
			}
			// Sentinel server-side resolution errors: surface and exit 2.
			var hee *httpErrExit2
			if errors.As(err, &hee) {
				return hee
			}
			if !gotConnectedOnce {
				// First-connect failure: fail-fast.
				return err
			}
			// Mid-stream drop, retry path: backoff (with keystroke wake).
			if !reconnect {
				return err
			}
			if !waitBackoff(ctx, shared, "redial failed") {
				return nil // ctx cancelled during backoff = clean detach
			}
			continue
		}

		// Connected. Reset backoff and send Hello with current size.
		shared.resetTier()
		c, r := unpackSize(size.Load())
		err = runSession(ctx, conn, c, r, shared)
		_ = conn.Close()

		if ctx.Err() != nil {
			// User detach / signal — clean exit regardless of err.
			return nil
		}
		gotConnectedOnce = true
		if !reconnect {
			// In non-reconnect mode, treat EOF / closed as clean
			// detach (matches local-socket "child exited" semantics).
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Reconnect mode: ANY conn termination (including EOF from a
		// bridge dying mid-stream) is a drop we should retry. If the
		// session is genuinely gone, the next dial returns 404 and we
		// surface exit 2.
		// Drop. Print disconnected line; wait for backoff.
		fmt.Fprintf(os.Stderr,
			"\r\n\x1b[2m[supervise: disconnected: %v]\x1b[0m\r\n", err)
		if !waitBackoff(ctx, shared, "") {
			return nil // ctx cancelled during backoff = clean detach
		}
		// Drain any pending wake before redial so the new connection
		// doesn't see a stale wake.
		select {
		case <-shared.wake:
		default:
		}
	}
}

// runSession runs a single connected session over conn. Owns the
// per-session writer goroutine and server-read loop. Sends the Hello
// frame as the first thing on the wire. Returns nil on clean server
// EOF, an error otherwise.
func runSession(
	ctx context.Context,
	conn net.Conn,
	cols, rows uint16,
	shared *sharedSession,
) error {
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

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
		case <-sessionCtx.Done():
			return false
		}
	}

	// Wire send into the shared bus *before* Hello so Size frames
	// from a SIGWINCH that fired between connect and Hello aren't
	// lost. (Order doesn't matter as long as Hello goes first; our
	// channel buffers up to 256 so an early Size queues behind.)
	shared.setSend(send)
	defer shared.clearSend()

	// Hello.
	helloPayload, _ := json.Marshal(attachwire.Hello{
		Cols: cols,
		Rows: rows,
	})
	if !send(attachwire.MsgHello, helloPayload) {
		return errors.New("ctx cancelled before Hello")
	}

	// Server-read loop (in this goroutine, since we own the lifetime).
	r := bufio.NewReader(conn)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runServerLoop(sessionCtx, r)
	}()

	// Wait for either side to finish.
	var sessErr error
	select {
	case err := <-serverDone:
		sessErr = err
	case <-ctx.Done():
		// Outer cancel; tear down.
	}

	sessionCancel()
	_ = conn.Close()
	close(outCh)
	<-writerDone
	// Drain serverDone if it hasn't fired yet.
	select {
	case <-serverDone:
	default:
	}

	// EOF / closed conn are not nil-ed out here — runConnectLoop
	// decides per the reconnect flag (mid-stream bridge death looks
	// like EOF on the TCP side, but in reconnect=false mode we still
	// want to treat it as a clean detach for backwards compat with
	// the local-socket path's "child exited → supervisor closed" semantics).
	return sessErr
}

// runStdinFSM reads stdin one byte at a time and runs the prefix-key
// state machine. When connected, byte events are forwarded as
// MsgInput frames; when disconnected, they're dropped silently and
// signal a reconnect-wake. <prefix>. cancels the outer ctx.
//
// Command vocabulary mirrors mosh:
//
//	<prefix> .       detach (cancel ctx)
//	<prefix> ^       send a literal prefix byte to the remote
//	<prefix> Ctrl-Z  SIGTSTP self (resume with fg)
//	<prefix> ?       print one-line help on stderr
func runStdinFSM(ctx context.Context, prefix byte, shared *sharedSession, cancel context.CancelFunc) {
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
		stateNormal    = 0
		stateAfterPref = 1
	)
	state := stateNormal
	for {
		select {
		case <-ctx.Done():
			return
		case rr := <-readCh:
			if rr.err != nil {
				return
			}
			b := rr.b
			switch state {
			case stateNormal:
				if b == prefix {
					state = stateAfterPref
					continue
				}
				shared.sendInputOrWake(b)
			case stateAfterPref:
				switch b {
				case '^':
					// Literal prefix: send if connected, drop if not
					// (no wake — this is data, not a "user is here" signal).
					shared.sendIfConnected(attachwire.MsgInput, []byte{prefix})
				case '.':
					cancel()
					return
				case 0x1a: // Ctrl-Z: suspend self, resume with fg
					_ = syscall.Kill(os.Getpid(), syscall.SIGTSTP)
				case '?':
					fmt.Fprintln(os.Stderr,
						"\r\nsupervise attach commands: \".\" detach, \"^\" literal prefix, \"Ctrl-Z\" suspend, \"?\" help\r")
				default:
					// silent ignore
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

// sharedSession is the cross-goroutine bus connecting the stdin/
// SIGWINCH/wake machinery to the *current* connection's writer.
// When sendFn is nil, the session is disconnected and inputs are
// dropped (with a wake signal for stdin events).
type sharedSession struct {
	mu     sync.Mutex
	sendFn func(typ byte, payload []byte) bool
	wake   chan struct{}
	tier   atomic.Int32 // current backoff tier; 0 = first retry == 1s
}

func (s *sharedSession) setSend(fn func(byte, []byte) bool) {
	s.mu.Lock()
	s.sendFn = fn
	s.mu.Unlock()
}

func (s *sharedSession) clearSend() {
	s.mu.Lock()
	s.sendFn = nil
	s.mu.Unlock()
}

// sendInputOrWake: if connected, forward the byte as MsgInput; if
// not, drop and signal a wake (the user is at the keyboard, retry now).
func (s *sharedSession) sendInputOrWake(b byte) {
	s.mu.Lock()
	fn := s.sendFn
	s.mu.Unlock()
	if fn != nil {
		fn(attachwire.MsgInput, []byte{b})
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// sendIfConnected sends the frame if a session exists; otherwise drops
// it without waking (used for SIGWINCH and literal-prefix bytes).
func (s *sharedSession) sendIfConnected(typ byte, payload []byte) {
	s.mu.Lock()
	fn := s.sendFn
	s.mu.Unlock()
	if fn != nil {
		fn(typ, payload)
	}
}

// waitBackoff sleeps for the next backoff tier (capped at 30s),
// rewriting the disconnect-status line in place each second so the
// user sees the countdown advance. Returns true on timer expiry,
// false if ctx cancels. A keystroke on shared.wake also returns true
// (immediate redial).
//
// State is held in a method-local closure: each call advances the
// schedule one tier. Reset by exiting waitBackoff after a successful
// connect (caller drains shared.wake).
var backoffSchedule = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

// backoffTier is incremented by waitBackoff; reset to 0 on success.
// Stored on the shared session so it persists across calls.
func waitBackoff(ctx context.Context, shared *sharedSession, _ string) bool {
	tier := int(shared.tier.Add(1) - 1)
	if tier >= len(backoffSchedule) {
		tier = len(backoffSchedule) - 1
	}
	d := backoffSchedule[tier]
	deadline := time.Now().Add(d)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			return true
		}
		// Print/refresh the status line in place.
		secs := int((remain + time.Second - 1) / time.Second)
		fmt.Fprintf(os.Stderr,
			"\r\x1b[2K\x1b[2m[supervise: reconnecting in %ds — press any key to retry now]\x1b[0m",
			secs)
		// Sleep up to 1s so we can refresh the countdown.
		step := time.Second
		if remain < step {
			step = remain
		}
		select {
		case <-ctx.Done():
			fmt.Fprint(os.Stderr, "\r\x1b[2K")
			return false
		case <-shared.wake:
			fmt.Fprint(os.Stderr, "\r\x1b[2K")
			return true
		case <-time.After(step):
		}
	}
}

// resetTier zeroes the backoff schedule. Called after each successful
// session connect.
func (s *sharedSession) resetTier() { s.tier.Store(0) }

// httpErrExit2 is the dialer's sentinel for server-side resolve
// errors (404 no-match, 409 ambiguous). The connect loop maps it to
// exit code 2.
type httpErrExit2 struct {
	msg string
}

func (e *httpErrExit2) Error() string { return e.msg }

// localDialer returns a dialFn for the local rpc.sock case.
func localDialer(sockPath string) dialFn {
	return func(ctx context.Context) (net.Conn, error) {
		conn, err := dialUnixSock(ctx, sockPath)
		if err != nil {
			return nil, err
		}
		if err := upgradeAttachConn(conn, "http://supervise/attach"); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// remoteDialer returns a dialFn for the --host case. base is the
// already-parsed http(s)://host:port URL; shortID is the user-typed
// argument passed verbatim to the server for resolution.
func remoteDialer(base *url.URL, shortID string) dialFn {
	return func(ctx context.Context) (net.Conn, error) {
		hostport := base.Host
		var conn net.Conn
		var err error
		switch base.Scheme {
		case "http":
			var d net.Dialer
			conn, err = d.DialContext(ctx, "tcp", hostport)
		case "https":
			d := &tls.Dialer{
				Config: &tls.Config{ServerName: hostFromHostport(hostport)},
			}
			conn, err = d.DialContext(ctx, "tcp", hostport)
		default:
			return nil, fmt.Errorf("unsupported scheme %q", base.Scheme)
		}
		if err != nil {
			return nil, err
		}
		target := *base
		target.Path = "/sessions/" + shortID + "/attach-raw"
		if err := upgradeAttachConn(conn, target.String()); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// upgradeAttachConn writes the supervise-attach/1 HTTP/1.1 Upgrade
// request to conn and reads the response. On 101 the conn is left
// positioned at the first attachwire byte. On 404 / 409 returns a
// *httpErrExit2 sentinel; on other non-101 returns a generic error.
func upgradeAttachConn(conn net.Conn, urlStr string) error {
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Upgrade", "supervise-attach/1")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(bufrw); err != nil {
		return fmt.Errorf("write upgrade: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		return fmt.Errorf("flush upgrade: %w", err)
	}
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		return fmt.Errorf("read upgrade response: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
		// Hand back any bytes the bufio reader may have over-read.
		// We can't easily push them back into conn; instead, callers
		// of this function must use a fresh bufio.Reader on conn for
		// frame parsing. Since attachwire is server-initiated after
		// the upgrade, in practice bufrw.Reader has 0 buffered bytes
		// here. (We assert this via Buffered().)
		if n := bufrw.Reader.Buffered(); n > 0 {
			return fmt.Errorf("internal: %d bytes buffered after upgrade response", n)
		}
		return nil
	case http.StatusNotFound:
		body, _ := io.ReadAll(resp.Body)
		return &httpErrExit2{msg: strings.TrimSpace(string(body))}
	case http.StatusConflict:
		body, _ := io.ReadAll(resp.Body)
		return &httpErrExit2{msg: strings.TrimSpace(string(body))}
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server refused upgrade: %s\n%s", resp.Status, string(body))
	}
}

// dialUnixSock dials a unix socket, with a long-path fallback (chdir
// then bind a relative path). Mirrors the server-side dialSock.
func dialUnixSock(ctx context.Context, sockPath string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err == nil {
		return conn, nil
	}
	// Fallback: chdir to make the socket path short.
	cwd, gerr := os.Getwd()
	if gerr != nil {
		return nil, err
	}
	if cerr := os.Chdir(filepath.Dir(sockPath)); cerr != nil {
		return nil, err
	}
	defer os.Chdir(cwd)
	return d.DialContext(ctx, "unix", filepath.Base(sockPath))
}

// parseHostFlag normalizes the --host argument into an http(s) URL.
// Accepts:
//
//	"m4mini:20000"        → http://m4mini:20000
//	"http://m4mini:20000" → http://m4mini:20000
//	"https://m4mini:443"  → https://m4mini:443
//
// Path / query are stripped (we always target /sessions/.../attach-raw).
func parseHostFlag(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("--host: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("--host: unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("--host: missing host")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

// hostFromHostport returns the host part of host:port for the TLS
// SNI / cert-verify ServerName field.
func hostFromHostport(hp string) string {
	if h, _, err := net.SplitHostPort(hp); err == nil {
		return h
	}
	return hp
}

// localSize returns local terminal cols/rows, or (0,0) if stdin is
// not a tty.
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

// localOrDefaultSize returns local terminal size, falling back to
// 80×24 if stdin is not a tty (scripted attaches).
func localOrDefaultSize() (uint16, uint16) {
	c, r := localSize()
	if c == 0 || r == 0 {
		return 80, 24
	}
	return c, r
}

// packSize / unpackSize fold (cols, rows) into a single uint64 so
// they can ride an atomic.Uint64 without a separate mutex.
func packSize(cols, rows uint16) uint64 {
	return uint64(cols)<<16 | uint64(rows)
}

func unpackSize(v uint64) (uint16, uint16) {
	return uint16(v >> 16), uint16(v & 0xffff)
}
