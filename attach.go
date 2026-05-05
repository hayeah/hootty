package hootty

import (
	"sync"
)

// attachWinsize is the {cols,rows} pair declared by a single attach.
// Stored separately from the kernel-level winsize so the AttachSet
// can keep all members' declared sizes around for the min-wins
// recompute.
type attachWinsize struct {
	Cols uint16
	Rows uint16
}

// attachConn is the server's view of one connected attach. It is an
// opaque token to AttachSet — the handler owns the framed-write
// goroutine and just hands AttachSet a callback to deliver Size
// frames.
type attachConn struct {
	id        uint64
	sendSize  func(attachWinsize) // closure into the conn's writer goroutine
	closeWith func(error)         // tear down the conn (not used for size events)
}

// AttachSet tracks all currently-connected attaches and their
// declared window sizes. The PTY has exactly one winsize; to keep
// every attach's view non-truncated we take min(cols) and min(rows)
// across the set and TIOCSWINSZ the master whenever the set changes.
//
// Recompute is triggered on Add / Remove / Update. Whenever the
// effective size changes, AttachSet calls sizeApplier(eff) to push
// the new size to the kernel + emulator, and broadcasts the new
// effective size to every attach via its sendSize callback.
//
// AttachSet does no I/O of its own; it's pure bookkeeping.
type AttachSet struct {
	mu      sync.Mutex
	members map[*attachConn]attachWinsize
	current attachWinsize // last broadcast effective; zero means "no broadcast yet"
	nextID  uint64

	// sizeApplier pushes effective onto the kernel-level winsize +
	// the emulator. Called under AttachSet's lock; must not block on
	// other attaches.
	sizeApplier func(attachWinsize)
}

// NewAttachSet constructs an AttachSet. apply is called whenever the
// effective size changes; typically wraps LibghosttyPTY.Resize.
func NewAttachSet(apply func(cols, rows uint16)) *AttachSet {
	return &AttachSet{
		members: make(map[*attachConn]attachWinsize),
		sizeApplier: func(w attachWinsize) {
			apply(w.Cols, w.Rows)
		},
	}
}

// nextAttachID returns a unique id for an attachConn. Caller takes
// the AttachSet lock.
func (a *AttachSet) nextAttachID() uint64 {
	a.nextID++
	return a.nextID
}

// Add registers a new member with its initial declared size, applies
// the recomputed effective size if it changed, and broadcasts the
// new size to all members. Returns the effective size after recompute
// (so the caller can include it in the immediate Size frame to the
// new attach).
func (a *AttachSet) Add(c *attachConn, ws attachWinsize) attachWinsize {
	a.mu.Lock()
	c.id = a.nextAttachID()
	a.members[c] = ws
	eff, changed := a.recomputeLocked()
	if changed {
		a.sizeApplier(eff)
	}
	// Snapshot send list under the lock to avoid sending while holding.
	var receivers []*attachConn
	if changed {
		receivers = make([]*attachConn, 0, len(a.members))
		for m := range a.members {
			if m != c { // new attach gets its initial Size from the handler directly
				receivers = append(receivers, m)
			}
		}
	}
	a.mu.Unlock()
	for _, m := range receivers {
		m.sendSize(eff)
	}
	return eff
}

// Update changes a member's declared size and triggers recompute /
// broadcast as needed. Returns the (possibly new) effective size.
func (a *AttachSet) Update(c *attachConn, ws attachWinsize) attachWinsize {
	a.mu.Lock()
	if _, ok := a.members[c]; !ok {
		a.mu.Unlock()
		return a.current
	}
	a.members[c] = ws
	eff, changed := a.recomputeLocked()
	if changed {
		a.sizeApplier(eff)
	}
	var receivers []*attachConn
	if changed {
		receivers = make([]*attachConn, 0, len(a.members))
		for m := range a.members {
			receivers = append(receivers, m)
		}
	}
	a.mu.Unlock()
	for _, m := range receivers {
		m.sendSize(eff)
	}
	return eff
}

// Remove drops a member and triggers recompute. If the effective
// size changed (because the dropped member was the cols-min or
// rows-min), broadcasts the new size to remaining members.
//
// Spec edge case: when the set goes empty we keep the last effective
// size. Matches tmux: panes don't shrink to zero when the client
// detaches. A re-attach from a smaller tty just shrinks once.
func (a *AttachSet) Remove(c *attachConn) {
	a.mu.Lock()
	if _, ok := a.members[c]; !ok {
		a.mu.Unlock()
		return
	}
	delete(a.members, c)
	eff, changed := a.recomputeLocked()
	var receivers []*attachConn
	if changed {
		a.sizeApplier(eff)
		receivers = make([]*attachConn, 0, len(a.members))
		for m := range a.members {
			receivers = append(receivers, m)
		}
	}
	a.mu.Unlock()
	for _, m := range receivers {
		m.sendSize(eff)
	}
}

// Effective returns the current min-wins size. Zero if the set is
// empty and nothing has ever been broadcast.
func (a *AttachSet) Effective() attachWinsize {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// recomputeLocked computes min(cols), min(rows) across members.
// Empty set → keep current (last-value-wins per spec). Updates
// a.current if it differs and returns (eff, changed).
//
// Caller must hold a.mu.
func (a *AttachSet) recomputeLocked() (attachWinsize, bool) {
	if len(a.members) == 0 {
		// Keep current; nothing to broadcast.
		return a.current, false
	}
	var eff attachWinsize
	first := true
	for _, ws := range a.members {
		if first {
			eff = ws
			first = false
			continue
		}
		if ws.Cols < eff.Cols {
			eff.Cols = ws.Cols
		}
		if ws.Rows < eff.Rows {
			eff.Rows = ws.Rows
		}
	}
	if eff != a.current {
		a.current = eff
		return eff, true
	}
	return eff, false
}
