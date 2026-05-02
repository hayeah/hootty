package supervisor

// queryStripper drops terminal-query escape sequences from a stream
// of bytes flowing FROM the child PTY TO an attached client's real
// terminal. The recorder + emulator path still sees raw bytes — only
// the subscriber fanout is filtered.
//
// Why: a terminal "query" is an RPC where the program writes an
// escape sequence and expects the terminal to write a reply back into
// the program's stdin (DA, DSR, CPR, XTVERSION, OSC color queries,
// kitty-keyboard query, ENQ, …). Inside `supervise attach`, the
// libghostty emulator on the supervisor already answers these; if we
// also fan the queries out to the user's real ghostty, real ghostty
// answers AGAIN and those reply bytes inject as input into the
// child's stdin. The child gets two replies for every query; the
// extras sit in the input buffer and get echoed by the line
// discipline when the next program (zsh) reads them in cooked mode —
// the visible "junk chars after tmux detach" symptom.
//
// The set is finite and stable. We don't need to fully parse VT — we
// just recognize-and-skip a known list of byte patterns. Anything we
// don't recognize as a query passes through unchanged.
//
// Stripped sequences:
//
//   - 0x05 ENQ — single byte
//   - CSI c / CSI ? c / CSI > c / CSI = c — DA1/DA2/DA3 device attrs
//   - CSI > q — XTVERSION
//   - CSI 5 n / CSI ? 5 n — DSR status
//   - CSI 6 n / CSI ? 6 n — CPR / DECXCPR cursor position report
//   - CSI ? Pn $ p — DECRQM mode query
//   - CSI 14 t / CSI 16 t / CSI 18 t / CSI 19 t — window/cell/screen size queries
//   - CSI ? u — kitty keyboard query
//   - OSC 4 ; n ; ? ST — palette color query
//   - OSC 10 ; ? ST / OSC 11 ; ? ST / OSC 12 ; ? ST — fg/bg/cursor color queries
//
// OSC color SETs (no `?` payload) MUST pass through — a child
// legitimately changing bg color should reach the user's terminal.
//
// State is buffered across chunk boundaries: a sequence that begins
// in chunk N and finishes in chunk N+1 is recognized correctly. The
// stripper holds at most one in-flight escape sequence's worth of
// bytes at a time.

// vtQueryStripper is a streaming filter. Feed bytes via Write; bytes
// that survived the filter are appended to the returned slice.
//
// Not safe for concurrent use; one stripper per subscriber.
type vtQueryStripper struct {
	state stripState
	// pending buffers a partial escape sequence that started in a
	// previous chunk. When the sequence finishes (drop) or turns out
	// not to be a query (flush passthrough), pending is reset.
	pending []byte
}

type stripState int

const (
	stNormal     stripState = iota // outside any escape
	stEsc                          // saw 0x1b, awaiting next byte
	stCSI                          // inside CSI: ESC [ … <final>
	stOSC                          // inside OSC: ESC ] … <ST|BEL>
	stOSCEscSeen                   // inside OSC, saw 0x1b, awaiting '\\'
)

// Filter returns the bytes to forward downstream. The returned slice
// is a fresh buffer each call (callers may retain it).
func (s *vtQueryStripper) Filter(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for _, b := range in {
		out = s.feed(b, out)
	}
	return out
}

// feed advances the state machine by one byte. Bytes that pass the
// filter are appended to out and the (possibly grown) slice is
// returned.
//
// The trick: we don't know if a partially-seen sequence is a query
// or some other ESC sequence (e.g. SGR `ESC[0m`) until we see the
// final byte. So we buffer everything from ESC onward into pending.
// When the sequence completes:
//   - if it matches a query shape, drop pending (output nothing).
//   - otherwise, flush pending to out unchanged.
//
// 7-bit and 8-bit C1 forms are both handled (0x9b for CSI, 0x9d for
// OSC), though child PTYs in practice use 7-bit ESC-prefixed forms.
func (s *vtQueryStripper) feed(b byte, out []byte) []byte {
	switch s.state {
	case stNormal:
		switch b {
		case 0x05: // ENQ — drop, no state change
			return out
		case 0x1b: // ESC
			s.state = stEsc
			s.pending = append(s.pending[:0], b)
			return out
		case 0x9b: // 8-bit CSI
			s.state = stCSI
			s.pending = append(s.pending[:0], b)
			return out
		case 0x9d: // 8-bit OSC
			s.state = stOSC
			s.pending = append(s.pending[:0], b)
			return out
		}
		return append(out, b)

	case stEsc:
		s.pending = append(s.pending, b)
		switch b {
		case '[':
			s.state = stCSI
			return out
		case ']':
			s.state = stOSC
			return out
		case 0x05:
			// ENQ inside an ESC sequence — flush ESC, drop ENQ, reset.
			out = append(out, 0x1b)
			s.state = stNormal
			s.pending = s.pending[:0]
			return out
		}
		// Any other byte: not a CSI/OSC start. Flush pending
		// unchanged and reset. (ESC + final-byte sequences like
		// charset selection ESC ( B etc. are not queries; we forward
		// them. Some need an additional byte — for our purposes it's
		// fine to just stream them through one byte at a time.)
		s.state = stNormal
		out = append(out, s.pending...)
		s.pending = s.pending[:0]
		return out

	case stCSI:
		s.pending = append(s.pending, b)
		// CSI grammar: parameter bytes 0x30-0x3f, intermediate bytes
		// 0x20-0x2f, final byte 0x40-0x7e. A final byte ends the
		// sequence.
		if b >= 0x40 && b <= 0x7e {
			drop := isCSIQuery(s.pending)
			s.state = stNormal
			if drop {
				s.pending = s.pending[:0]
				return out
			}
			out = append(out, s.pending...)
			s.pending = s.pending[:0]
			return out
		}
		// Not yet final. Sanity cap on a runaway buffer (a malformed
		// stream shouldn't grow pending without bound).
		if len(s.pending) > 256 {
			s.state = stNormal
			out = append(out, s.pending...)
			s.pending = s.pending[:0]
		}
		return out

	case stOSC:
		// OSC terminates on BEL (0x07) or ST (ESC \\ / 0x9c).
		switch b {
		case 0x07: // BEL terminator
			s.pending = append(s.pending, b)
			drop := isOSCQuery(s.pending)
			s.state = stNormal
			if drop {
				s.pending = s.pending[:0]
				return out
			}
			out = append(out, s.pending...)
			s.pending = s.pending[:0]
			return out
		case 0x1b:
			s.state = stOSCEscSeen
			// Don't append yet; we'll decide once we see what follows.
			return out
		case 0x9c: // 8-bit ST
			s.pending = append(s.pending, b)
			drop := isOSCQuery(s.pending)
			s.state = stNormal
			if drop {
				s.pending = s.pending[:0]
				return out
			}
			out = append(out, s.pending...)
			s.pending = s.pending[:0]
			return out
		}
		s.pending = append(s.pending, b)
		if len(s.pending) > 1024 {
			// Runaway OSC; bail out and forward what we have.
			s.state = stNormal
			out = append(out, s.pending...)
			s.pending = s.pending[:0]
		}
		return out

	case stOSCEscSeen:
		if b == '\\' {
			// ST. Treat the ESC \\ as the terminator.
			s.pending = append(s.pending, 0x1b, '\\')
			drop := isOSCQuery(s.pending)
			s.state = stNormal
			if drop {
				s.pending = s.pending[:0]
				return out
			}
			out = append(out, s.pending...)
			s.pending = s.pending[:0]
			return out
		}
		// Unexpected: the OSC didn't terminate cleanly. Treat the
		// stray ESC as starting a new escape and reprocess b.
		// Forward the buffered OSC bytes, reset, then feed ESC + b.
		out = append(out, s.pending...)
		s.pending = s.pending[:0]
		s.state = stNormal
		out = s.feed(0x1b, out)
		out = s.feed(b, out)
		return out
	}
	return out
}

// isCSIQuery returns true if buf is a complete CSI sequence (starting
// with 0x1b '[' or 0x9b, ending in a final byte 0x40-0x7e) that we
// recognize as a terminal query and want to drop.
func isCSIQuery(buf []byte) bool {
	// Strip the introducer to leave parameters + intermediates +
	// final.
	body := stripCSIIntroducer(buf)
	if len(body) == 0 {
		return false
	}
	final := body[len(body)-1]
	params := body[:len(body)-1]

	switch final {
	case 'c':
		// CSI c / CSI ? c / CSI > c / CSI = c — DA1/2/3.
		// Parameters are at most one of '?', '>', '=' followed
		// optionally by a numeric "0" (DA1 spec allows CSI 0 c).
		return paramsAreDA(params)
	case 'n':
		// DSR / CPR. Drop CSI 5 n, CSI 6 n, CSI ? 5 n, CSI ? 6 n.
		return paramsAreDSR(params)
	case 'q':
		// XTVERSION: CSI > q. Don't strip CSI Pn SP q (cursor
		// style) — note SP is an intermediate, so params would end
		// in 0x20.
		if len(params) == 1 && params[0] == '>' {
			return true
		}
		return false
	case 't':
		// Window ops queries: CSI 14 t / 16 t / 18 t / 19 t.
		// Window ops can be SETs too (e.g. CSI 4 ; H ; W t resizes);
		// only the size-report queries are pure-query.
		return paramsAreSizeQuery(params)
	case 'p':
		// DECRQM: CSI ? Pn $ p (note '$' is intermediate, included
		// in params).
		return paramsAreDECRQM(params)
	case 'u':
		// Kitty keyboard query: CSI ? u (no params other than '?').
		// CSI u alone is "restore cursor" (xterm) — keep that.
		// CSI > 0 u etc. is push/pop; not a query.
		return len(params) == 1 && params[0] == '?'
	}
	return false
}

// stripCSIIntroducer removes the leading CSI introducer (either
// 0x1b '[' or single 0x9b) and returns the rest.
func stripCSIIntroducer(buf []byte) []byte {
	if len(buf) >= 2 && buf[0] == 0x1b && buf[1] == '[' {
		return buf[2:]
	}
	if len(buf) >= 1 && buf[0] == 0x9b {
		return buf[1:]
	}
	return buf
}

// paramsAreDA matches the DA1/DA2/DA3 parameter shapes:
// "" (DA1), "0" (DA1), "?" (DA1 with private), ">" (DA2), "=" (DA3).
func paramsAreDA(params []byte) bool {
	switch string(params) {
	case "", "0", "?", ">", "=":
		return true
	}
	return false
}

// paramsAreDSR matches DSR / CPR / DECXCPR shapes: "5" "6" "?5" "?6".
func paramsAreDSR(params []byte) bool {
	switch string(params) {
	case "5", "6", "?5", "?6":
		return true
	}
	return false
}

// paramsAreSizeQuery matches CSI 14 t / 16 t / 18 t / 19 t.
func paramsAreSizeQuery(params []byte) bool {
	switch string(params) {
	case "14", "16", "18", "19":
		return true
	}
	return false
}

// paramsAreDECRQM matches CSI ? Pn $ — i.e. starts with '?', ends
// with '$' (intermediate), middle is a numeric mode id.
func paramsAreDECRQM(params []byte) bool {
	if len(params) < 3 {
		return false
	}
	if params[0] != '?' || params[len(params)-1] != '$' {
		return false
	}
	for _, c := range params[1 : len(params)-1] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isOSCQuery returns true if buf is a complete OSC sequence we
// recognize as a color-query and want to drop. OSC body shape:
//
//	OSC <Ps> ; <payload> ST
//
// We drop when Ps is one of {4, 10, 11, 12} and the payload contains
// a '?' (the query marker). Plain SET (`OSC 11 ; rgb:...` ST) is
// passed through.
func isOSCQuery(buf []byte) bool {
	body := stripOSCIntroducer(buf)
	body = stripOSCTerminator(body)
	// Split on ';'. The first element is Ps; the rest is payload.
	semi := -1
	for i, c := range body {
		if c == ';' {
			semi = i
			break
		}
	}
	if semi < 0 {
		return false
	}
	ps := string(body[:semi])
	payload := body[semi+1:]
	switch ps {
	case "10", "11", "12":
		// Drop iff the payload starts with '?' (single query) or
		// contains '?' as a value.
		return oscPayloadIsQuery(payload)
	case "4":
		// OSC 4 ; n ; ? — the payload is "n;?". Drop iff after
		// stripping the n and its semicolon we get a '?' or
		// chained '?'s.
		return oscPayloadIsQuery(payload)
	}
	return false
}

// oscPayloadIsQuery returns true when the OSC payload contains a '?'
// segment — i.e. the program is asking, not setting. We accept both
// `?` and `n;?` shapes (the OSC 4 family takes a palette index).
func oscPayloadIsQuery(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	// Walk segments separated by ';'. If any segment is exactly "?",
	// it's a query.
	start := 0
	for i := 0; i <= len(payload); i++ {
		if i == len(payload) || payload[i] == ';' {
			seg := payload[start:i]
			if len(seg) == 1 && seg[0] == '?' {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// stripOSCIntroducer removes ESC ] (or 0x9d) from the front.
func stripOSCIntroducer(buf []byte) []byte {
	if len(buf) >= 2 && buf[0] == 0x1b && buf[1] == ']' {
		return buf[2:]
	}
	if len(buf) >= 1 && buf[0] == 0x9d {
		return buf[1:]
	}
	return buf
}

// stripOSCTerminator removes BEL (0x07), ST (0x9c), or ESC \\ from
// the end.
func stripOSCTerminator(buf []byte) []byte {
	n := len(buf)
	if n == 0 {
		return buf
	}
	if buf[n-1] == 0x07 || buf[n-1] == 0x9c {
		return buf[:n-1]
	}
	if n >= 2 && buf[n-2] == 0x1b && buf[n-1] == '\\' {
		return buf[:n-2]
	}
	return buf
}
