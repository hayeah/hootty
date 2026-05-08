package session

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
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

// TestSpacerBridgesLongIdle confirms the type=3 spacer path:
// a fixture with a single long-idle gap is read back with the
// correct cumulative timestamp on the next frame.
func TestSpacerBridgesLongIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	// Hand-build: prelude resize, type=3 spacer (advance ~70s),
	// type=1 output at delta=33ms beyond the spacer.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString(FrameMagic + "\n\n"); err != nil {
		t.Fatalf("preamble: %v", err)
	}
	// Prelude resize.
	f.Write(frameHeader(FrameTypeResize, 0, 4))
	f.Write(encResize(80, 24))
	// Spacer for 70_000 ms.
	f.Write(frameHeader(FrameTypeSpacer, 0, 4))
	var spacer [4]byte
	binary.LittleEndian.PutUint32(spacer[:], 70_000)
	f.Write(spacer[:])
	// Output frame at delta=33ms after the spacer.
	f.Write(frameHeader(FrameTypeOutput, 33, 5))
	f.Write([]byte("hello"))
	f.Close()

	r, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	dec := NewFrameDecoder(r)
	// Skip prelude.
	if _, err := dec.Next(); err != nil {
		t.Fatalf("prelude: %v", err)
	}
	// Spacer is silently consumed; next call returns the output frame.
	fr, err := dec.Next()
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if fr.Type != FrameTypeOutput {
		t.Fatalf("type=%d, want %d", fr.Type, FrameTypeOutput)
	}
	wantAt := 70_033 * time.Millisecond
	if fr.At != wantAt {
		t.Fatalf("At = %v, want %v", fr.At, wantAt)
	}
	if string(fr.Payload) != "hello" {
		t.Fatalf("payload = %q", fr.Payload)
	}
}

// TestRecorderEmitsSpacerOnLongIdle verifies the writer side: a
// recorder whose started clock is far in the past produces a spacer
// frame ahead of the next output frame.
func TestRecorderEmitsSpacerOnLongIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Backdate the recorder so any subsequent write is ~70s into the
	// recording — well beyond the uint16 ms cap.
	rec.started = time.Now().Add(-70 * time.Second)
	if _, err := rec.Write([]byte("late")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r2, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r2.Close()
	dec := NewFrameDecoder(r2)
	// Prelude resize.
	if _, err := dec.Next(); err != nil {
		t.Fatalf("prelude: %v", err)
	}
	// Output frame: At should reflect >= 65535ms from the spacer
	// advance plus a small residual delta. Spacer itself is consumed
	// internally by FrameDecoder.
	fr, err := dec.Next()
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if fr.Type != FrameTypeOutput {
		t.Fatalf("type=%d", fr.Type)
	}
	if fr.At < 65*time.Second {
		t.Fatalf("At = %v, want >= 65s (spacer + residual)", fr.At)
	}
	if string(fr.Payload) != "late" {
		t.Fatalf("payload = %q", fr.Payload)
	}
}

// TestAsciicastRoundtripPayloadIdentity exercises the binary →
// asciicast → payload-identity contract. Walk recorded output bytes,
// transcode to asciicast, parse back, and the concatenated `o` event
// payloads must equal the originally-recorded bytes.
func TestAsciicastRoundtripPayloadIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	chunks := [][]byte{
		[]byte("first chunk\r\n"),
		[]byte("\x1b[31mred\x1b[0m\r\n"),
		[]byte("done\r\n"),
	}
	var want bytes.Buffer
	for _, c := range chunks {
		if _, err := rec.Write(c); err != nil {
			t.Fatalf("Write: %v", err)
		}
		want.Write(c)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer in.Close()
	var out bytes.Buffer
	if err := WriteAsciicastJSONL(in, &out, AsciicastHeader{Version: 2}); err != nil {
		t.Fatalf("WriteAsciicastJSONL: %v", err)
	}

	// Concatenate every output-event payload.
	scanner := bufio.NewScanner(&out)
	first := true
	var got bytes.Buffer
	for scanner.Scan() {
		if first {
			first = false
			continue // skip header
		}
		var ev []any
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("event parse: %v", err)
		}
		if kind, _ := ev[1].(string); kind == "o" {
			if data, _ := ev[2].(string); data != "" {
				got.WriteString(data)
			}
		}
	}
	if got.String() != want.String() {
		t.Fatalf("roundtrip payload mismatch:\n got %q\nwant %q", got.String(), want.String())
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
