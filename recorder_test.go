package session

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecorderWritesBinaryFormat exercises the on-disk shape of the
// hootty.log v1 file: ASCII preamble, prelude type=2 resize at ts=0,
// and a flushed type=1 frame after Close.
func TestRecorderWritesBinaryFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Write([]byte("hello\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.RecordResize(100, 30); err != nil {
		t.Fatalf("RecordResize: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	preamble := []byte(FrameMagic + "\n\n")
	if !bytes.HasPrefix(raw, preamble) {
		t.Fatalf("missing preamble; got %q", raw[:min(len(raw), 64)])
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	dec := NewFrameDecoder(f)

	// First frame: prelude resize at ts=0 with cols=120 rows=40.
	fr, err := dec.Next()
	if err != nil {
		t.Fatalf("decode prelude: %v", err)
	}
	if fr.Type != FrameTypeResize {
		t.Fatalf("prelude type=%d, want %d", fr.Type, FrameTypeResize)
	}
	if fr.At != 0 {
		t.Fatalf("prelude At=%v, want 0", fr.At)
	}
	cols := binary.LittleEndian.Uint16(fr.Payload[0:2])
	rows := binary.LittleEndian.Uint16(fr.Payload[2:4])
	if cols != 120 || rows != 40 {
		t.Fatalf("prelude size %dx%d, want 120x40", cols, rows)
	}

	// Second frame: type=1 output carrying "hello\r\n".
	fr, err = dec.Next()
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if fr.Type != FrameTypeOutput {
		t.Fatalf("output type=%d", fr.Type)
	}
	if string(fr.Payload) != "hello\r\n" {
		t.Fatalf("output payload = %q, want %q", fr.Payload, "hello\r\n")
	}

	// Third frame: type=2 resize 100x30.
	fr, err = dec.Next()
	if err != nil {
		t.Fatalf("decode resize: %v", err)
	}
	if fr.Type != FrameTypeResize {
		t.Fatalf("resize type=%d", fr.Type)
	}
	cols = binary.LittleEndian.Uint16(fr.Payload[0:2])
	rows = binary.LittleEndian.Uint16(fr.Payload[2:4])
	if cols != 100 || rows != 30 {
		t.Fatalf("resize size %dx%d, want 100x30", cols, rows)
	}

	if _, err := dec.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// TestReadOutputEventsBoundsWindow drives the test-only helper over a
// hand-built fixture to confirm window slicing.
func TestReadOutputEventsBoundsWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	frames := []frameSpec{
		{typ: FrameTypeResize, deltaMs: 0, payload: encResize(80, 24)},
		{typ: FrameTypeOutput, deltaMs: 0, payload: []byte("old")},
		{typ: FrameTypeOutput, deltaMs: 20_000, payload: []byte("new")},
	}
	if err := writeFixture(path, frames); err != nil {
		t.Fatalf("writeFixture: %v", err)
	}

	events, err := ReadOutputEvents(path, 5*time.Second)
	if err != nil {
		t.Fatalf("ReadOutputEvents: %v", err)
	}
	if len(events) != 1 || string(events[0].Data) != "new" {
		t.Fatalf("events = %#v, want only recent output", events)
	}
}

// TestRecorderAutoSplitsLargeBucket exercises the > 64 KiB split path:
// the recorder emits chained type=1 frames at ts_delta_ms=0 for the
// remainder.
func TestRecorderAutoSplitsLargeBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Write enough to span 2 chunks: 65535 + 100 = 65635 bytes.
	big := bytes.Repeat([]byte{'x'}, 65535+100)
	if _, err := rec.Write(big); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	dec := NewFrameDecoder(f)

	// Skip prelude resize.
	if _, err := dec.Next(); err != nil {
		t.Fatalf("decode prelude: %v", err)
	}

	fr1, err := dec.Next()
	if err != nil {
		t.Fatalf("decode first chunk: %v", err)
	}
	fr2, err := dec.Next()
	if err != nil {
		t.Fatalf("decode second chunk: %v", err)
	}
	if fr1.Type != FrameTypeOutput || fr2.Type != FrameTypeOutput {
		t.Fatalf("expected two output frames, got %d %d", fr1.Type, fr2.Type)
	}
	if len(fr1.Payload) != 65535 || len(fr2.Payload) != 100 {
		t.Fatalf("split sizes = %d + %d, want 65535 + 100", len(fr1.Payload), len(fr2.Payload))
	}
	// Follow-on frame must reuse the first frame's timestamp.
	if fr1.At != fr2.At {
		t.Fatalf("follow-on frame At=%v, want equal to first %v", fr2.At, fr1.At)
	}
}

// TestFrameDecoderRejectsBadMagic ensures readers refuse files
// without the magic preamble.
func TestFrameDecoderRejectsBadMagic(t *testing.T) {
	dec := NewFrameDecoder(bytes.NewReader([]byte("not a hootty file\n\n")))
	if _, err := dec.Next(); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

// TestFrameDecoderTruncatedFrameIsEOF — partial trailing frame
// (recorder killed mid-write) is treated as clean EOF.
func TestFrameDecoderTruncatedFrameIsEOF(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(FrameMagic + "\n\n")
	// Emit one valid output frame.
	hdr := frameHeader(FrameTypeOutput, 0, 3)
	buf.Write(hdr)
	buf.Write([]byte("abc"))
	// Then write a truncated header — only 3 bytes of the next 5.
	buf.Write([]byte{FrameTypeOutput, 0, 0})

	dec := NewFrameDecoder(&buf)
	fr, err := dec.Next()
	if err != nil || string(fr.Payload) != "abc" {
		t.Fatalf("first frame: fr=%#v err=%v", fr, err)
	}
	if _, err := dec.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF on truncated frame, got %v", err)
	}
}

// frameSpec describes one fixture frame in tests.
type frameSpec struct {
	typ     uint8
	deltaMs uint16
	payload []byte
}

func frameHeader(typ uint8, deltaMs uint16, length uint16) []byte {
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.LittleEndian.PutUint16(hdr[1:3], deltaMs)
	binary.LittleEndian.PutUint16(hdr[3:5], length)
	return hdr
}

func encResize(cols, rows uint16) []byte {
	p := make([]byte, 4)
	binary.LittleEndian.PutUint16(p[0:2], cols)
	binary.LittleEndian.PutUint16(p[2:4], rows)
	return p
}

func writeFixture(path string, frames []frameSpec) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(FrameMagic + "\n\n"); err != nil {
		return err
	}
	for _, fr := range frames {
		// uint16 deltaMs is bounded by construction; callers that
		// need to model long idles emit explicit type=3 spacer
		// frames in the fixture.
		hdr := frameHeader(fr.typ, fr.deltaMs, uint16(len(fr.payload)))
		if _, err := f.Write(hdr); err != nil {
			return err
		}
		if len(fr.payload) > 0 {
			if _, err := f.Write(fr.payload); err != nil {
				return err
			}
		}
	}
	return nil
}
