package hootty

import (
	"bytes"
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
// Construction flow: the parent process (e.g. `hoot run`) opens
// a PTY pair via github.com/creack/pty, forks the hootty with
// Stdin/Stdout/Stderr = slave and ExtraFiles = [master]. Inside the
// hootty, the master arrives at fd 3:
//
//	master := os.NewFile(3, "pty-master")
//	pty, err := NewLibghosttyPTY(master, cols, rows, opts...)
//
// The Service's exec.Cmd.Start inherits stdio from the hootty, so
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
	// primaryTerm mirrors only the primary screen. It receives the
	// child's output after alternate-screen mode switches and
	// alternate-screen content are stripped.
	primaryTerm   *libghostty.Terminal
	primaryFilter vtPrimaryScreenFilter

	mu   sync.RWMutex
	cols uint16
	rows uint16

	// actions are single-threaded-access closures against term +
	// subs. Send-only from outside the dispatcher goroutine.
	actions chan func()

	subs map[chan []byte]*subscriber // owned by dispatcher goroutine
	rec  *Recorder                   // owned by dispatcher goroutine

	doneOnce sync.Once
	done     chan struct{}
}

// subscriber bundles a per-attach query stripper with its delivery
// channel. The stripper holds streaming state across chunk
// boundaries: an escape sequence that begins in chunk N and
// finishes in chunk N+1 is recognized correctly. One stripper per
// subscriber so per-attach state never crosses connections.
type subscriber struct {
	ch       chan []byte
	stripper vtQueryStripper
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

// WithRecorder attaches an output recorder. Every chunk the
// dispatcher receives is also written to the recorder before fanout.
func WithRecorder(rec *Recorder) LibghosttyOption {
	return func(o *libghosttyOptions) { o.rec = rec }
}

// NewLibghosttyPTY wraps an existing PTY master file. Caller retains
// ownership of master's lifecycle: closing master on shutdown is the
// caller's job (typically the hootty's Run flow closes it via
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
		subs:    make(map[chan []byte]*subscriber),
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

	primaryTerm, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollback(o.scrollback),
	)
	if err != nil {
		term.Close()
		return nil, fmt.Errorf("new primary terminal: %w", err)
	}
	p.primaryTerm = primaryTerm

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
			for ch, sub := range p.subs {
				delete(p.subs, ch)
				close(sub.ch)
			}
			if p.term != nil {
				p.term.Close()
				p.term = nil
			}
			if p.primaryTerm != nil {
				p.primaryTerm.Close()
				p.primaryTerm = nil
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
				if p.primaryTerm != nil {
					primaryChunk := p.primaryFilter.Filter(chunk)
					if len(primaryChunk) > 0 {
						p.primaryTerm.VTWrite(primaryChunk)
					}
				}
				// Per-subscriber strip pass: filter out terminal-
				// query escape sequences (DA/DSR/CPR/XTVERSION/...)
				// before fanout. The recorder + emulator above see
				// the unfiltered bytes; only the user's real terminal
				// is shielded from re-answering queries that the
				// emulator already answered. See vtquery_stripper.go.
				for _, sub := range p.subs {
					filtered := sub.stripper.Filter(chunk)
					if len(filtered) == 0 {
						continue
					}
					select {
					case sub.ch <- filtered:
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
// advisory and currently ignored: libghostty's formatter walks the
// emulator's full active screen — viewport plus scrollback up to
// max_scrollback rows — and there's no row-range slicing knob. The
// scrollback ring cap is the only way to bound output. withEscapes
// =true emits VT-replayable bytes (FormatterFormatVT); false emits
// plain text (FormatterFormatPlain).
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
// hootty process must NOT share the child's session/ctty —
// i.e. the process holding the master is session-less for this tty,
// and the child does Setctty to claim the slave. See cmd/hoot
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
		if err := p.term.Resize(cols, rows, 0, 0); err != nil {
			termErr = err
			return
		}
		if p.primaryTerm != nil {
			if err := p.primaryTerm.Resize(cols, rows, 0, 0); err != nil {
				termErr = err
				return
			}
		}
		if p.rec != nil {
			_ = p.rec.RecordResize(cols, rows)
		}
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
//
// Includes scrollback (up to max_scrollback × cols), the cursor
// position, the active SGR style, and any non-default terminal modes,
// so a fresh terminal repaints the same picture the hootty sees.
//
// Known limitation: when the child is on the alt screen (tmux, vim,
// less, etc.) the snapshot is just the alt-screen contents — the
// primary scrollback the hootty recorded is preserved internally
// but invisible to the formatter, and when the child later exits alt
// the user's terminal restores its own (empty) primary. See
// ~/Dropbox/notes/2026-05-02/hoot-alt-screen-scrollback-gap_claude.md.
func (p *LibghosttyPTY) Snapshot() ([]byte, error) {
	var out []byte
	var ferr error
	p.do(func() {
		out, ferr = p.snapshotLocked()
	})
	return out, ferr
}

// SnapshotParts returns the current main-screen snapshot split into
// scrollback history and visible-screen replay bytes.
//
// The scrollback part is intended to be printed into the attaching
// terminal's scrollback ring before the client clears the viewport.
// The screen part is then painted onto that blank viewport and includes
// the formatter's cursor/style/mode restore bytes.
//
// Alternate-screen snapshots intentionally retain the legacy full
// snapshot in the screen part. Alt-screen attach has different
// invariants and is handled by a separate protocol pass; preserving the
// old primary+alternate replay avoids regressing live alt-screen exit.
func (p *LibghosttyPTY) SnapshotParts() (scrollback, screen []byte, err error) {
	p.do(func() {
		scrollback, screen, err = p.snapshotPartsLocked()
	})
	return scrollback, screen, err
}

func (p *LibghosttyPTY) snapshotLocked() ([]byte, error) {
	if p.term == nil {
		return nil, errors.New("pty closed")
	}
	active, err := p.term.ActiveScreen()
	if err != nil {
		return nil, err
	}
	if active != libghostty.ScreenAlternate || p.primaryTerm == nil {
		return formatTerminalSnapshot(p.term)
	}
	primary, err := formatTerminalSnapshot(p.primaryTerm)
	if err != nil {
		return nil, err
	}
	alt, err := formatTerminalSnapshot(p.term)
	if err != nil {
		return nil, err
	}
	return append(primary, alt...), nil
}

func (p *LibghosttyPTY) snapshotPartsLocked() ([]byte, []byte, error) {
	if p.term == nil {
		return nil, nil, errors.New("pty closed")
	}
	active, err := p.term.ActiveScreen()
	if err != nil {
		return nil, nil, err
	}
	if active == libghostty.ScreenAlternate {
		snap, err := p.snapshotLocked()
		return nil, snap, err
	}
	return formatTerminalSnapshotParts(p.term)
}

func formatTerminalSnapshot(term *libghostty.Terminal) ([]byte, error) {
	f, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterTrim(true),
		libghostty.WithFormatterExtraCursor(true),
		libghostty.WithFormatterExtraStyle(true),
		libghostty.WithFormatterExtraModes(true),
	)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Format()
}

func formatTerminalSnapshotParts(term *libghostty.Terminal) ([]byte, []byte, error) {
	snap, err := formatTerminalSnapshot(term)
	if err != nil {
		return nil, nil, err
	}
	scrollbackRows, err := term.ScrollbackRows()
	if err != nil {
		return nil, nil, err
	}
	scrollback, screen := splitSnapshotRows(snap, scrollbackRows)
	return scrollback, screen, nil
}

func splitSnapshotRows(snap []byte, scrollbackRows uint) (scrollback, screen []byte) {
	if scrollbackRows == 0 || len(snap) == 0 {
		return nil, snap
	}
	rest := snap
	cut := 0
	for i := uint(0); i < scrollbackRows; i++ {
		idx := bytes.Index(rest, []byte("\r\n"))
		if idx < 0 {
			return snap, nil
		}
		cut += idx + len("\r\n")
		rest = rest[idx+len("\r\n"):]
	}
	return snap[:cut], snap[cut:]
}

// subscribe registers a channel to receive live PTY bytes. Returns
// the channel and a cancel function.
func (p *LibghosttyPTY) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	sub := &subscriber{ch: ch}
	p.do(func() {
		p.subs[ch] = sub
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

// SubscribeAtRecord registers a live subscriber on the dispatcher
// goroutine. Cancel removes the subscription and closes the channel.
func (p *LibghosttyPTY) SubscribeAtRecord() (<-chan []byte, func()) {
	ch := make(chan []byte, 256)
	sub := &subscriber{ch: ch}
	p.do(func() {
		p.subs[ch] = sub
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

// SubscribeWithSnapshot registers a live subscriber and captures a
// snapshot in one dispatcher action. The returned snapshot reflects
// all PTY bytes processed before the subscription was installed; live
// chunks delivered on the channel are strictly after that snapshot.
func (p *LibghosttyPTY) SubscribeWithSnapshot() (<-chan []byte, []byte, func(), error) {
	ch := make(chan []byte, 256)
	sub := &subscriber{ch: ch}
	var snap []byte
	var snapErr error
	ran := false
	p.do(func() {
		ran = true
		if p.term == nil {
			snapErr = errors.New("pty closed")
			return
		}
		p.subs[ch] = sub
		snap, snapErr = p.snapshotLocked()
		if snapErr != nil {
			delete(p.subs, ch)
			close(ch)
		}
	})
	if !ran {
		close(ch)
		return nil, nil, nil, errors.New("pty closed")
	}
	if snapErr != nil {
		return nil, nil, nil, snapErr
	}
	cancel := func() {
		p.do(func() {
			if _, ok := p.subs[ch]; ok {
				delete(p.subs, ch)
				close(ch)
			}
		})
	}
	return ch, snap, cancel, nil
}

// SubscribeWithSnapshotParts registers a live subscriber and captures a
// split snapshot in one dispatcher action. The returned parts reflect
// all PTY bytes processed before the subscription was installed; live
// chunks delivered on the channel are strictly after that snapshot.
func (p *LibghosttyPTY) SubscribeWithSnapshotParts() (<-chan []byte, []byte, []byte, func(), error) {
	ch := make(chan []byte, 256)
	sub := &subscriber{ch: ch}
	var scrollback []byte
	var screen []byte
	var snapErr error
	ran := false
	p.do(func() {
		ran = true
		if p.term == nil {
			snapErr = errors.New("pty closed")
			return
		}
		p.subs[ch] = sub
		scrollback, screen, snapErr = p.snapshotPartsLocked()
		if snapErr != nil {
			delete(p.subs, ch)
			close(ch)
		}
	})
	if !ran {
		close(ch)
		return nil, nil, nil, nil, errors.New("pty closed")
	}
	if snapErr != nil {
		return nil, nil, nil, nil, snapErr
	}
	cancel := func() {
		p.do(func() {
			if _, ok := p.subs[ch]; ok {
				delete(p.subs, ch)
				close(ch)
			}
		})
	}
	return ch, scrollback, screen, cancel, nil
}

// Recorder returns the recorder configured on this PTY (may be nil).
func (p *LibghosttyPTY) Recorder() *Recorder { return p.rec }

// RegisterRoutes contributes /pty/{text,html,vt,stream,input,resize}
// and /attach to the hootty's mux. Runner.Run invokes this
// automatically.
func (p *LibghosttyPTY) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/pty/text", p.handleText)
	mux.HandleFunc("/pty/html", p.handleHTML)
	mux.HandleFunc("/pty/vt", p.handleVT)
	mux.HandleFunc("/pty/stream", p.handleStream)
	mux.HandleFunc("/pty/input", p.handleInput)
	mux.HandleFunc("/pty/resize", p.handleResize)

	// /attach upgrades to the binary attach protocol from
	// internal/attachwire. One handler instance is shared by all
	// attaches; per-attach state lives in the goroutine that runs
	// serveOne.
	ah := newAttachHandler(p)
	mux.Handle("/attach", ah)
}
