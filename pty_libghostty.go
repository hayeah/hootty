package supervisor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/creack/pty"
	libghostty "github.com/mitchellh/go-libghostty"
)

// LibghosttyPTY owns a PTY master fd and a libghostty Terminal that
// parses every byte the child writes. The single-dispatcher pattern
// is required: the underlying libghostty Terminal is not safe for
// concurrent mutation (per the binding's docs — VTWrite / Resize /
// Reset / mode setters and reads must serialise on a single
// goroutine).
//
// Construction flow: the parent process (e.g. `supervise run`) opens
// a PTY pair via github.com/creack/pty, forks the supervisor with
// Stdin/Stdout/Stderr = slave and ExtraFiles = [master]. Inside the
// supervisor, the master arrives at fd 3:
//
//	master := os.NewFile(3, "pty-master")
//	pty, err := NewLibghosttyPTY(master, cols, rows, opts...)
//
// The Service's exec.Cmd.Start inherits stdio from the supervisor, so
// the child ends up inside the PTY automatically.
//
// Recorder: the dispatcher tees every chunk it receives to the
// configured Recorder before fanning out to live subscribers. Tee
// happens inside the dispatcher so subscribers and the recorder see
// byte-identical streams.
//
// The PTY master itself is fine to Write from multiple goroutines
// (kernel handles that), so Write doesn't go through the dispatcher.
type LibghosttyPTY struct {
	master *os.File
	term   *libghostty.Terminal

	mu   sync.RWMutex
	cols uint16
	rows uint16

	// actions are single-threaded-access closures against term +
	// subs. Send-only from outside the dispatcher goroutine.
	actions chan func()

	subs map[chan []byte]struct{} // owned by dispatcher goroutine
	rec  *Recorder                // owned by dispatcher goroutine

	doneOnce sync.Once
	done     chan struct{}
}

// LibghosttyOption configures a LibghosttyPTY at construction.
type LibghosttyOption func(*libghosttyOptions)

type libghosttyOptions struct {
	scrollback uint
	rec        *Recorder
}

// WithLibghosttyScrollback sets the libghostty Terminal's scrollback
// line budget. Default is 10_000 lines (libghostty itself defaults
// to 0 — no scrollback at all — which would be surprising).
func WithLibghosttyScrollback(lines uint) LibghosttyOption {
	return func(o *libghosttyOptions) { o.scrollback = lines }
}

// WithRecorder attaches a raw-byte tee. Every chunk the dispatcher
// receives is also written to the recorder before fanout.
func WithRecorder(rec *Recorder) LibghosttyOption {
	return func(o *libghosttyOptions) { o.rec = rec }
}

// NewLibghosttyPTY wraps an existing PTY master file. Caller retains
// ownership of master's lifecycle: closing master on shutdown is the
// caller's job (typically the supervisor's Run flow closes it via
// LibghosttyPTY.Close → which only stops the dispatcher; the master
// fd close is the caller's).
//
// cols and rows are the initial screen size and should match the
// kernel-level winsize the caller set on the master via
// pty.Setsize before handing it to this constructor.
func NewLibghosttyPTY(master *os.File, cols, rows uint16, opts ...LibghosttyOption) (*LibghosttyPTY, error) {
	if master == nil {
		return nil, errors.New("libghostty: master is nil")
	}
	o := libghosttyOptions{scrollback: 10_000}
	for _, opt := range opts {
		opt(&o)
	}

	p := &LibghosttyPTY{
		master:  master,
		cols:    cols,
		rows:    rows,
		actions: make(chan func(), 64),
		subs:    make(map[chan []byte]struct{}),
		rec:     o.rec,
		done:    make(chan struct{}),
	}

	// WithWritePty closes the loop on terminal queries (DA/DECRPM/
	// cursor-pos). Without it, the child's VT probes silently
	// disappear and apps like vim break on first probe. Master
	// writes are kernel-safe to do from any goroutine.
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollback(o.scrollback),
		libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) {
			_, _ = p.master.Write(append([]byte(nil), data...))
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("new terminal: %w", err)
	}
	p.term = term

	go p.dispatch()
	go p.readLoop()
	return p, nil
}

// Close stops the dispatcher: closes outstanding subscriber
// channels, tears down the Terminal, and closes the recorder if
// one was set. The master file is NOT closed here — the caller
// owns it. Safe to call multiple times.
func (p *LibghosttyPTY) Close() error {
	p.doneOnce.Do(func() {
		shutdown := make(chan struct{})
		select {
		case p.actions <- func() {
			for ch := range p.subs {
				delete(p.subs, ch)
				close(ch)
			}
			if p.term != nil {
				p.term.Close()
				p.term = nil
			}
			if p.rec != nil {
				_ = p.rec.Close()
			}
			close(shutdown)
		}:
			<-shutdown
		default:
		}
		close(p.done)
	})
	return nil
}

// dispatch is the single goroutine that owns term + subs + recorder.
// All Feed / Resize / format / subscribe / unsubscribe routes
// through here. Drains queued actions on Close so callers don't
// hang on their done channel.
func (p *LibghosttyPTY) dispatch() {
	for {
		select {
		case <-p.done:
			for {
				select {
				case action := <-p.actions:
					action()
				default:
					return
				}
			}
		case action := <-p.actions:
			action()
		}
	}
}

// do runs fn on the dispatcher and waits for it to complete. If
// the PTY is closed, do returns without running fn.
func (p *LibghosttyPTY) do(fn func()) {
	done := make(chan struct{})
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.actions <- func() {
		fn()
		close(done)
	}:
		<-done
	case <-p.done:
		return
	}
}

// readLoop reads from the master, tees to the recorder, feeds the
// emulator, and fans out to /pty/stream subscribers. Exits when the
// master is closed (EOF) or the PTY is Close()d.
func (p *LibghosttyPTY) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			chunk := append([]byte{}, buf[:n]...)
			p.do(func() {
				if p.rec != nil {
					_, _ = p.rec.Write(chunk)
				}
				if p.term != nil {
					p.term.VTWrite(chunk)
				}
				for ch := range p.subs {
					select {
					case ch <- chunk:
					default:
						// Slow subscriber: drop. They re-sync on
						// reattach via the snapshot prefix.
					}
				}
			})
		}
		if err != nil {
			if err != io.EOF {
				// File-already-closed during shutdown is the common
				// non-EOF case; nothing actionable.
			}
			return
		}
	}
}

// Write sends raw bytes to the PTY master. Safe to call from any
// goroutine; does not touch the emulator (the emulator only sees
// bytes that come back from the child — user input produces echo
// that flows through readLoop in the normal terminal model).
func (p *LibghosttyPTY) Write(data []byte) error {
	_, err := p.master.Write(data)
	return err
}

// Capture returns a textual dump of the current screen. lines is
// advisory and currently ignored (libghostty's formatter formats the
// whole terminal — there is no row-range API). withEscapes=true
// emits VT-replayable bytes (FormatterFormatVT); false emits plain
// text (FormatterFormatPlain).
func (p *LibghosttyPTY) Capture(lines int, withEscapes bool) (string, error) {
	_ = lines
	format := libghostty.FormatterFormatPlain
	if withEscapes {
		format = libghostty.FormatterFormatVT
	}
	return p.formatString(format)
}

// FormatHTML emits a self-contained HTML fragment for the current
// terminal state via libghostty's FormatterFormatHTML. Cheaper than
// HTMLString — buffer-sized, no extra string conversion.
func (p *LibghosttyPTY) FormatHTML() ([]byte, error) {
	return p.formatBytes(libghostty.FormatterFormatHTML)
}

// FormatText emits plain text (no escapes).
func (p *LibghosttyPTY) FormatText() ([]byte, error) {
	return p.formatBytes(libghostty.FormatterFormatPlain)
}

// FormatVT emits VT-replayable bytes — feed back into a fresh
// terminal and the screen reproduces.
func (p *LibghosttyPTY) FormatVT() ([]byte, error) {
	return p.formatBytes(libghostty.FormatterFormatVT)
}

func (p *LibghosttyPTY) formatBytes(format libghostty.FormatterFormat) ([]byte, error) {
	var out []byte
	var ferr error
	p.do(func() {
		if p.term == nil {
			ferr = errors.New("pty closed")
			return
		}
		f, err := libghostty.NewFormatter(p.term,
			libghostty.WithFormatterFormat(format),
			libghostty.WithFormatterTrim(true),
		)
		if err != nil {
			ferr = err
			return
		}
		defer f.Close()
		out, ferr = f.Format()
	})
	return out, ferr
}

func (p *LibghosttyPTY) formatString(format libghostty.FormatterFormat) (string, error) {
	b, err := p.formatBytes(format)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Resize updates both the kernel-level PTY winsize and the
// emulator's grid. TIOCSWINSZ goes on the master fd; for this to
// keep working across the child's job-control handoff, the
// supervisor process must NOT share the child's session/ctty —
// i.e. the process holding the master is session-less for this tty,
// and the child does Setctty to claim the slave. See cmd/supervise
// for how that's wired.
func (p *LibghosttyPTY) Resize(cols, rows uint16) error {
	if err := pty.Setsize(p.master, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return fmt.Errorf("setsize: %w", err)
	}
	var termErr error
	p.do(func() {
		if p.term == nil {
			termErr = errors.New("pty closed")
			return
		}
		// cellWidthPx/cellHeightPx are only used by Kitty graphics;
		// 0 is fine for headless setups.
		termErr = p.term.Resize(cols, rows, 0, 0)
	})
	if termErr != nil {
		return fmt.Errorf("term resize: %w", termErr)
	}
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	return nil
}

// Size returns the current (cols, rows). Convenience for HTTP
// handlers.
func (p *LibghosttyPTY) Size() (cols, rows uint16) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cols, p.rows
}

// Snapshot returns the current screen as VT-replayable bytes — the
// first chunk of /pty/stream so subscribers render a faithful screen
// before live bytes arrive.
func (p *LibghosttyPTY) Snapshot() ([]byte, error) {
	return p.FormatVT()
}

// subscribe registers a channel to receive live PTY bytes. Returns
// the channel and a cancel function.
func (p *LibghosttyPTY) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	p.do(func() {
		p.subs[ch] = struct{}{}
	})
	cancel := func() {
		p.do(func() {
			if _, ok := p.subs[ch]; ok {
				delete(p.subs, ch)
				close(ch)
			}
		})
	}
	return ch, cancel
}

// RegisterRoutes contributes /pty/{text,html,vt,stream,input,resize}
// to the supervisor's mux. Runner.Run invokes this automatically.
func (p *LibghosttyPTY) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/pty/text", p.handleText)
	mux.HandleFunc("/pty/html", p.handleHTML)
	mux.HandleFunc("/pty/vt", p.handleVT)
	mux.HandleFunc("/pty/stream", p.handleStream)
	mux.HandleFunc("/pty/input", p.handleInput)
	mux.HandleFunc("/pty/resize", p.handleResize)
}
