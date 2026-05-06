package session

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/hayeah/hootty/internal/attachwire"
)

func TestClientReadLoopAnswersPing(t *testing.T) {
	var in bytes.Buffer
	if err := attachwire.WriteFrame(&in, attachwire.MsgPing, nil); err != nil {
		t.Fatalf("write ping fixture: %v", err)
	}

	var got []byte
	h := &attachHandler{}
	err := h.clientReadLoop(
		bufio.NewReadWriter(bufio.NewReader(&in), bufio.NewWriter(io.Discard)),
		&attachConn{},
		func(typ byte, payload []byte) {
			got = append(got, typ)
			if len(payload) != 0 {
				t.Fatalf("pong payload length = %d, want 0", len(payload))
			}
		},
	)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("clientReadLoop err = %v, want EOF", err)
	}
	if len(got) != 1 || got[0] != attachwire.MsgPong {
		t.Fatalf("sent frames = %#v, want one Pong", got)
	}
}
