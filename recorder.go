package session

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	defaultRecorderCols = 80
	defaultRecorderRows = 24

	// FrameMaxPayload is the per-frame payload cap (uint16 length).
	FrameMaxPayload = 1 << 16

	// frameBucketMs is the recorder batching window. ~30 fps. The
	// recorder coalesces all output bytes within a bucket into a
	// single type=1 frame (or chained frames if > 64 KiB).
	frameBucketMs = 33

	// FrameMagic is the first line of every recording. Readers MUST
	// reject the file if the magic line doesn't match exactly.
	FrameMagic = "# hootty.log v1"

	// recorderPreamble is the full preamble written before frames:
	// magic line + blank line.
	recorderPreamble = FrameMagic + "\n\n"
)

// Frame type constants for the binary recording format. The on-disk
// layout for every frame is uniform:
//
//	offset  size  field        notes
//	0       1     type         frame type
//	1       2     ts_delta_ms  uint16 LE — ms since previous frame
//	3       2     length       uint16 LE — payload length (max 64 KiB)
//	5       N     payload      N = length
//
// All multi-byte fields are little-endian.
const (
	// FrameTypeOutput carries PTY output bytes. Per-frame max 64 KiB;
	// oversized buckets split into chained frames at ts_delta_ms=0.
	FrameTypeOutput uint8 = 1

	// FrameTypeResize carries a 4-byte payload: uint16 cols (LE) +
	// uint16 rows (LE). The recording's first frame MUST be a resize
	// at ts=0 carrying the initial geometry.
	FrameTypeResize uint8 = 2

	// FrameTypeSpacer bridges idle gaps that exceed the 65535 ms
	// range of ts_delta_ms. Payload is 4 bytes — uint32 LE ms — and
	// the header's ts_delta_ms field is reserved zero.
	FrameTypeSpacer uint8 = 3
)

// Recorder appends binary frames to a hootty.log v1 file. The point
// is to keep an authoritative byte-faithful PTY log for libghostty
// replay and asciinema export.
//
// Concurrency: the dispatcher goroutine in LibghosttyPTY is the only
// writer in production. The mutex is defensive against tests that
// might write from multiple goroutines.
type Recorder struct {
	mu      sync.Mutex
	path    string
	w       io.WriteCloser
	started time.Time

	// lastEmittedMs is the ms-since-recording-start of the most
	// recent emitted frame. Used to compute ts_delta_ms (and to
	// decide whether a spacer is needed).
	lastEmittedMs uint64

	// pendingBucketMs is the bucket-start time (ms since recording
	// start) of the in-flight type=1 buffer. Zero means no pending
	// buffer.
	pendingBucketMs uint64
	// pendingHasBucket distinguishes "no pending output" from
	// "pending output for bucket=0" — the very first output frame's
	// bucket can legitimately be 0.
	pendingHasBucket bool
	pendingBuf       []byte
}

// NewRecorder opens (or creates+truncates) path and writes the
// hootty.log v1 preamble plus the prelude type=2 resize frame at
// ts=0. Subsequent Write calls append batched type=1 frames; resize
// events flush the pending buffer and append type=2 frames.
func NewRecorder(path string, cols, rows uint16) (*Recorder, error) {
	if cols == 0 {
		cols = defaultRecorderCols
	}
	if rows == 0 {
		rows = defaultRecorderRows
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		w:       f,
		path:    path,
		started: time.Now(),
	}
	if _, err := f.Write([]byte(recorderPreamble)); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Prelude resize at ts=0 — first-frame resize convention.
	if err := r.writeResizeFrameLocked(0, cols, rows); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

// Write appends data to the current bucket's output buffer and
// flushes one or more type=1 frames on bucket transition. Returns
// len(data) on success so Recorder still satisfies io.Writer.
func (r *Recorder) Write(data []byte) (int, error) {
	if r == nil || r.w == nil {
		return len(data), nil
	}
	if len(data) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowMsLocked()
	bucket := (now / frameBucketMs) * frameBucketMs

	if r.pendingHasBucket && bucket != r.pendingBucketMs {
		if err := r.flushPendingLocked(); err != nil {
			return 0, err
		}
	}
	if !r.pendingHasBucket {
		r.pendingBucketMs = bucket
		r.pendingHasBucket = true
	}
	r.pendingBuf = append(r.pendingBuf, data...)
	return len(data), nil
}

// RecordResize flushes any pending output buffer (preserving the
// witnessed output→resize order), then writes a type=2 resize frame.
func (r *Recorder) RecordResize(cols, rows uint16) error {
	if r == nil || r.w == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.flushPendingLocked(); err != nil {
		return err
	}
	now := r.nowMsLocked()
	if err := r.bridgeIdleLocked(now); err != nil {
		return err
	}
	delta := now - r.lastEmittedMs
	if err := r.writeResizeFrameLocked(uint16(delta), cols, rows); err != nil {
		return err
	}
	r.lastEmittedMs = now
	return nil
}

// Path returns the on-disk path of the recording.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Close flushes any pending output buffer and closes the underlying
// file. Safe to call multiple times.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil {
		return nil
	}
	flushErr := r.flushPendingLocked()
	closeErr := r.w.Close()
	r.w = nil
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (r *Recorder) nowMsLocked() uint64 {
	return uint64(time.Since(r.started).Milliseconds())
}

// bridgeIdleLocked emits a single type=3 spacer frame if the gap
// from lastEmittedMs to now exceeds 65535 ms. After emit,
// lastEmittedMs is advanced so that the next frame's ts_delta_ms
// fits in uint16. Idles longer than ~49.7 days chain multiple
// spacers.
func (r *Recorder) bridgeIdleLocked(nowMs uint64) error {
	for nowMs-r.lastEmittedMs > 65535 {
		gap := nowMs - r.lastEmittedMs
		var advance uint32
		if gap > uint64(^uint32(0)) {
			advance = ^uint32(0)
		} else {
			advance = uint32(gap)
		}
		var hdr [5]byte
		hdr[0] = FrameTypeSpacer
		// ts_delta_ms reserved zero on spacer frames.
		binary.LittleEndian.PutUint16(hdr[3:5], 4)
		var payload [4]byte
		binary.LittleEndian.PutUint32(payload[:], advance)
		if _, err := r.w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := r.w.Write(payload[:]); err != nil {
			return err
		}
		r.lastEmittedMs += uint64(advance)
	}
	return nil
}

// flushPendingLocked drains pendingBuf into one or more type=1
// frames at pendingBucketMs. Writes a type=3 spacer prelude if the
// gap to the bucket exceeds 65535 ms. No-op if no pending output.
func (r *Recorder) flushPendingLocked() error {
	if !r.pendingHasBucket {
		return nil
	}
	bucket := r.pendingBucketMs
	buf := r.pendingBuf
	r.pendingBucketMs = 0
	r.pendingHasBucket = false
	r.pendingBuf = nil

	if err := r.bridgeIdleLocked(bucket); err != nil {
		return err
	}
	delta := uint16(bucket - r.lastEmittedMs)
	first := true
	for {
		chunkLen := len(buf)
		if chunkLen > FrameMaxPayload-1 {
			// Cap one frame at uint16-max bytes — uint16 length
			// holds 0..65535, so the largest single payload is
			// 65535. Keep one byte under FrameMaxPayload (which is
			// 65536) so length always fits.
			chunkLen = FrameMaxPayload - 1
		}
		var hdr [5]byte
		hdr[0] = FrameTypeOutput
		var thisDelta uint16
		if first {
			thisDelta = delta
		}
		binary.LittleEndian.PutUint16(hdr[1:3], thisDelta)
		binary.LittleEndian.PutUint16(hdr[3:5], uint16(chunkLen))
		if _, err := r.w.Write(hdr[:]); err != nil {
			return err
		}
		if chunkLen > 0 {
			if _, err := r.w.Write(buf[:chunkLen]); err != nil {
				return err
			}
		}
		buf = buf[chunkLen:]
		first = false
		if len(buf) == 0 {
			break
		}
	}
	r.lastEmittedMs = bucket
	return nil
}

// writeResizeFrameLocked emits a type=2 frame with the given delta
// and a 4-byte cols+rows payload. Caller is responsible for
// updating lastEmittedMs if needed (the prelude frame at ts=0 sets
// lastEmittedMs=0 implicitly via Recorder construction).
func (r *Recorder) writeResizeFrameLocked(deltaMs, cols, rows uint16) error {
	var hdr [5]byte
	hdr[0] = FrameTypeResize
	binary.LittleEndian.PutUint16(hdr[1:3], deltaMs)
	binary.LittleEndian.PutUint16(hdr[3:5], 4)
	var payload [4]byte
	binary.LittleEndian.PutUint16(payload[0:2], cols)
	binary.LittleEndian.PutUint16(payload[2:4], rows)
	if _, err := r.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := r.w.Write(payload[:]); err != nil {
		return err
	}
	return nil
}

// Frame is one decoded record from a hootty.log v1 file.
type Frame struct {
	// Type is one of FrameTypeOutput, FrameTypeResize, FrameTypeSpacer.
	// Unknown types are returned as-is so callers can skip them.
	Type uint8

	// At is the absolute time since recording start, accumulated by
	// the decoder from per-frame deltas (and any spacer payloads).
	At time.Duration

	// Payload is the raw payload bytes. For FrameTypeResize this is
	// exactly 4 bytes (cols/rows); for FrameTypeSpacer it is exactly
	// 4 bytes (u32 ms). For FrameTypeOutput it's the chunk bytes.
	Payload []byte
}

// FrameDecoder iterates over a hootty.log v1 file. Calling Next
// returns the next decoded frame or io.EOF. Truncated trailing
// frames are silently treated as EOF (recorder may have been killed
// mid-frame).
type FrameDecoder struct {
	r              io.Reader
	preambleParsed bool
	cumMs          uint64
	hdr            [5]byte
	scratch        []byte
}

// NewFrameDecoder wraps r. It parses the ASCII preamble lazily on
// the first Next call.
func NewFrameDecoder(r io.Reader) *FrameDecoder {
	return &FrameDecoder{r: r}
}

// ErrBadMagic is returned by FrameDecoder.Next if the file's first
// line isn't the expected magic header.
var ErrBadMagic = errors.New("hootty.log: bad or missing magic line")

// Next returns the next frame. Returns io.EOF when the file is
// exhausted (or when a partial trailing frame is encountered).
func (d *FrameDecoder) Next() (Frame, error) {
	if !d.preambleParsed {
		if err := d.parsePreamble(); err != nil {
			return Frame{}, err
		}
		d.preambleParsed = true
	}
	// Read the 5-byte header; treat short read as EOF.
	if _, err := io.ReadFull(d.r, d.hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, io.EOF
		}
		return Frame{}, err
	}
	t := d.hdr[0]
	delta := binary.LittleEndian.Uint16(d.hdr[1:3])
	length := binary.LittleEndian.Uint16(d.hdr[3:5])

	if cap(d.scratch) < int(length) {
		d.scratch = make([]byte, length)
	} else {
		d.scratch = d.scratch[:length]
	}
	if length > 0 {
		if _, err := io.ReadFull(d.r, d.scratch); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return Frame{}, io.EOF
			}
			return Frame{}, err
		}
	}

	switch t {
	case FrameTypeSpacer:
		// Header delta MUST be zero on spacer frames; tolerate
		// non-zero per spec (readers ignore rather than fail).
		if length != 4 {
			return Frame{}, fmt.Errorf("hootty.log: spacer length=%d, want 4", length)
		}
		advance := binary.LittleEndian.Uint32(d.scratch[:4])
		d.cumMs += uint64(advance)
		// Spacers carry no replayable data; skip and recurse.
		return d.Next()
	default:
		d.cumMs += uint64(delta)
	}
	out := make([]byte, length)
	copy(out, d.scratch)
	return Frame{
		Type:    t,
		At:      time.Duration(d.cumMs) * time.Millisecond,
		Payload: out,
	}, nil
}

func (d *FrameDecoder) parsePreamble() error {
	// Read up to ~256 bytes looking for the first blank-line
	// boundary. The preamble is "# hootty.log v1\n\n" plus
	// optional future "# key=value" comment lines.
	var buf []byte
	tmp := make([]byte, 1)
	for {
		if len(buf) > 4096 {
			return ErrBadMagic
		}
		n, err := d.r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[0])
			// Detect blank-line terminator: "\n\n".
			if len(buf) >= 2 && buf[len(buf)-1] == '\n' && buf[len(buf)-2] == '\n' {
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return ErrBadMagic
			}
			return err
		}
	}
	// Validate magic: first line must equal FrameMagic.
	for i, b := range []byte(FrameMagic) {
		if i >= len(buf) || buf[i] != b {
			return ErrBadMagic
		}
	}
	if len(buf) <= len(FrameMagic) || buf[len(FrameMagic)] != '\n' {
		return ErrBadMagic
	}
	return nil
}

// AsciicastOutputEvent is one output event reconstructed from the
// binary log. Used by tests that want to inspect the recorded byte
// stream as discrete (timestamp, payload) events. Production replay
// goes through FrameDecoder + libghostty.
type AsciicastOutputEvent struct {
	At   time.Duration
	Data []byte
}

// ReadOutputEvents reads all type=1 (output) frames from path. If
// window is positive, only events from the final window duration
// are returned. Resize events are intentionally ignored.
//
// Kept as a thin helper on top of FrameDecoder for test
// convenience. Production replay uses FrameDecoder directly.
func ReadOutputEvents(path string, window time.Duration) ([]AsciicastOutputEvent, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	dec := NewFrameDecoder(f)
	var events []AsciicastOutputEvent
	var last time.Duration
	for {
		fr, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if fr.At > last {
			last = fr.At
		}
		if fr.Type != FrameTypeOutput {
			continue
		}
		events = append(events, AsciicastOutputEvent{
			At:   fr.At,
			Data: fr.Payload,
		})
	}
	if window <= 0 || len(events) == 0 {
		return events, nil
	}
	cutoff := last - window
	if cutoff <= 0 {
		return events, nil
	}
	i := 0
	for i < len(events) && events[i].At < cutoff {
		i++
	}
	return events[i:], nil
}

