// Package attachwire defines the tiny TLV protocol that
// `supervise attach` and the supervisor's attach handler speak over
// a single hijacked rpc.sock connection.
//
// Frame format:
//
//	+--------+----------+---------+
//	| u8 typ | u32 len  | payload |
//	+--------+----------+---------+
//
// `len` is little-endian. Four message types: Hello (C→S),
// Input (C→S), Output (S→C), Size (both directions).
//
// See `docs/tasks/<slug>/spec.md` (or the design note at
// supervise-attach-spec_claude.md) for the full protocol.
package attachwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Message type tags.
const (
	MsgHello  byte = 0x00
	MsgInput  byte = 0x01
	MsgOutput byte = 0x02
	MsgSize   byte = 0x03
)

// MaxPayload is a sanity cap on a single frame payload. The largest
// realistic frame is a full pty.log dumped during full-replay; we
// stream it in chunks well below this.
const MaxPayload = 1 << 24 // 16 MiB

// Hello is the first frame the client sends. The server answers
// with Output frames (and a Size{} broadcast); there is no Welcome.
type Hello struct {
	Cols       uint16 `json:"cols"`
	Rows       uint16 `json:"rows"`
	ReplayMode string `json:"replay_mode"` // "full" | "snapshot"
}

// Size is direction-overloaded. C→S: client declares its current
// local terminal size. S→C: server reports the negotiated remote
// PTY size after min-wins recompute.
type Size struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// WriteFrame writes a single TLV frame to w. Not safe for concurrent
// use; callers serialize writes through a single goroutine.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("attachwire: payload %d exceeds max %d", len(payload), MaxPayload)
	}
	var hdr [5]byte
	hdr[0] = typ
	binary.LittleEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads a single TLV frame from r. Returns io.EOF on a
// clean connection close right at a frame boundary.
func ReadFrame(r io.Reader) (typ byte, payload []byte, err error) {
	var hdr [5]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	typ = hdr[0]
	n := binary.LittleEndian.Uint32(hdr[1:])
	if n > MaxPayload {
		return 0, nil, fmt.Errorf("attachwire: frame length %d exceeds max %d", n, MaxPayload)
	}
	if n == 0 {
		return typ, nil, nil
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return typ, payload, nil
}

// ParsePrefixKey parses a prefix-key spec accepted by
// `--prefix-key`. Accepted forms:
//
//   - "C-b", "C-a", "^b", "^a"      → control-char shorthand
//   - "0x02", "0x1c"                → hex byte
//   - a single ASCII control char    (i.e. \x01..\x1f)
//
// Printable bytes are rejected: a printable prefix would silently
// swallow user typing, which is a very un-fun bug to debug.
func ParsePrefixKey(spec string) (byte, error) {
	if spec == "" {
		return 0, errors.New("prefix-key: empty")
	}
	// "C-x" / "c-x" / "^x"
	if (len(spec) == 3 && (spec[0] == 'C' || spec[0] == 'c') && spec[1] == '-') ||
		(len(spec) == 2 && spec[0] == '^') {
		var letter byte
		if len(spec) == 3 {
			letter = spec[2]
		} else {
			letter = spec[1]
		}
		switch {
		case letter >= 'a' && letter <= 'z':
			return letter - 'a' + 1, nil
		case letter >= 'A' && letter <= 'Z':
			return letter - 'A' + 1, nil
		case letter == '\\':
			return 0x1c, nil
		case letter == ']':
			return 0x1d, nil
		case letter == '^':
			return 0x1e, nil
		case letter == '_':
			return 0x1f, nil
		case letter == '@':
			return 0x00, nil
		case letter == '?':
			return 0x7f, nil
		default:
			return 0, fmt.Errorf("prefix-key: cannot map %q to a control byte", spec)
		}
	}
	// "0x.." hex
	if strings.HasPrefix(spec, "0x") || strings.HasPrefix(spec, "0X") {
		n, err := strconv.ParseUint(spec[2:], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("prefix-key: bad hex %q: %w", spec, err)
		}
		b := byte(n)
		if b >= 0x20 && b < 0x7f {
			return 0, fmt.Errorf("prefix-key %q is a printable byte; printable prefixes silently eat user typing", spec)
		}
		return b, nil
	}
	// Single ASCII control char: only accept literal control bytes.
	if len(spec) == 1 {
		b := spec[0]
		if b >= 0x20 && b < 0x7f {
			return 0, fmt.Errorf("prefix-key %q is a printable byte; printable prefixes silently eat user typing", spec)
		}
		return b, nil
	}
	return 0, fmt.Errorf("prefix-key: unrecognized form %q", spec)
}
