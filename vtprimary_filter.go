package supervisor

// vtPrimaryScreenFilter produces the byte stream for a primary-only
// mirror terminal. It strips alternate-screen mode switches from the
// stream and suppresses bytes written while the child is on the
// alternate screen. The mirror therefore tracks the primary screen as
// if tmux/vim/less had never taken over the display.
//
// It deliberately does not answer terminal queries; the real
// libghostty terminal remains the only query responder.
type vtPrimaryScreenFilter struct {
	inAlt   bool
	state   primaryFilterState
	pending []byte
}

type primaryFilterState int

const (
	primaryFilterNormal primaryFilterState = iota
	primaryFilterEsc
	primaryFilterCSI
)

func (f *vtPrimaryScreenFilter) Filter(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for _, b := range in {
		out = f.feed(b, out)
	}
	return out
}

func (f *vtPrimaryScreenFilter) feed(b byte, out []byte) []byte {
	switch f.state {
	case primaryFilterNormal:
		switch b {
		case 0x1b:
			f.state = primaryFilterEsc
			f.pending = append(f.pending[:0], b)
			return out
		case 0x9b:
			f.state = primaryFilterCSI
			f.pending = append(f.pending[:0], b)
			return out
		}
		if f.inAlt {
			return out
		}
		return append(out, b)

	case primaryFilterEsc:
		f.pending = append(f.pending, b)
		if b == '[' {
			f.state = primaryFilterCSI
			return out
		}
		f.state = primaryFilterNormal
		if f.inAlt {
			f.pending = f.pending[:0]
			return out
		}
		out = append(out, f.pending...)
		f.pending = f.pending[:0]
		return out

	case primaryFilterCSI:
		f.pending = append(f.pending, b)
		if b >= 0x40 && b <= 0x7e {
			altMode, enter := csiAltScreenMode(f.pending)
			f.state = primaryFilterNormal
			if altMode {
				f.inAlt = enter
				f.pending = f.pending[:0]
				return out
			}
			if f.inAlt {
				f.pending = f.pending[:0]
				return out
			}
			out = append(out, f.pending...)
			f.pending = f.pending[:0]
			return out
		}
		if len(f.pending) > 256 {
			f.state = primaryFilterNormal
			if !f.inAlt {
				out = append(out, f.pending...)
			}
			f.pending = f.pending[:0]
		}
		return out
	}
	return out
}

func csiAltScreenMode(buf []byte) (altMode bool, enter bool) {
	body := stripCSIIntroducer(buf)
	if len(body) < 3 {
		return false, false
	}
	final := body[len(body)-1]
	if final != 'h' && final != 'l' {
		return false, false
	}
	if body[0] != '?' {
		return false, false
	}
	params := body[1 : len(body)-1]
	for len(params) > 0 {
		next := len(params)
		for i, b := range params {
			if b == ';' {
				next = i
				break
			}
		}
		switch string(params[:next]) {
		case "47", "1047", "1049":
			return true, final == 'h'
		}
		if next == len(params) {
			break
		}
		params = params[next+1:]
	}
	return false, false
}
