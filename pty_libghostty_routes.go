package session

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// handleText returns the current screen as plain text via libghostty
// FormatterFormatPlain.
func (p *LibghosttyPTY) handleText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := p.FormatText()
	if err != nil {
		http.Error(w, "format: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(out)
}

// handleHTML returns the current screen as an HTML fragment via
// libghostty FormatterFormatHTML. The output is a single styled
// <div>; you wrap it yourself and supply the palette CSS.
func (p *LibghosttyPTY) handleHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := p.FormatHTML()
	if err != nil {
		http.Error(w, "format: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(out)
}

// handleVT returns the current screen as VT-replayable bytes —
// re-feed the result to a fresh terminal and the screen reproduces.
// This is what an `attach` CLI uses to seed its initial paint.
func (p *LibghosttyPTY) handleVT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := p.FormatVT()
	if err != nil {
		http.Error(w, "format: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(out)
}

// handleStream serves a chunked binary stream: first chunk is the
// VT snapshot of the current screen, subsequent chunks are live
// bytes from the child as they flow.
func (p *LibghosttyPTY) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe BEFORE taking the snapshot so any bytes arriving
	// between snapshot and first live delivery are queued on the
	// channel rather than lost. The dispatcher serializes subscribe
	// with feed; no bytes slip between them.
	ch, cancel := p.subscribe()
	defer cancel()

	snap, err := p.Snapshot()
	if err != nil {
		http.Error(w, "snapshot: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	if len(snap) > 0 {
		if _, err := w.Write(snap); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// inputBodyCap is the max body size accepted by handleInput. 1 MiB
// is far above any realistic shell paste; anything larger is almost
// certainly a runaway pipe. Bumpable later if a real use case appears.
const inputBodyCap = 1 << 20

// pasteStart and pasteEnd are the DEC bracketed-paste markers
// (DECSET 2004). The receiving program collects everything between
// them as one logical paste — no auto-execute on embedded newlines.
var (
	pasteStart = []byte{0x1b, '[', '2', '0', '0', '~'}
	pasteEnd   = []byte{0x1b, '[', '2', '0', '1', '~'}
)

// handleInput accepts POST /pty/input. Body is the raw bytes to send
// to the master FD. Query param ?paste=on opts into bracketed-paste
// wrapping; if the receiver does not have DECSET 2004 enabled, the
// request fails with 409 and writes nothing.
func (p *LibghosttyPTY) handleInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, inputBodyCap+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(data) > inputBodyCap {
		http.Error(w, "body too large (max 1 MiB)", http.StatusRequestEntityTooLarge)
		return
	}

	paste := r.URL.Query().Get("paste")
	switch paste {
	case "", "off":
		// no-op
	case "on":
		if bytes.Contains(data, pasteEnd) {
			http.Error(w, "payload contains bracketed-paste end sequence (\\e[201~)", http.StatusBadRequest)
			return
		}
		if !p.BracketedPasteActive() {
			http.Error(w, "bracketed paste not enabled on receiver", http.StatusConflict)
			return
		}
		wrapped := make([]byte, 0, len(pasteStart)+len(data)+len(pasteEnd))
		wrapped = append(wrapped, pasteStart...)
		wrapped = append(wrapped, data...)
		wrapped = append(wrapped, pasteEnd...)
		data = wrapped
	default:
		http.Error(w, "paste: must be on, off, or unset", http.StatusBadRequest)
		return
	}

	if err := p.Write(data); err != nil {
		http.Error(w, "write pty: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResize accepts JSON {cols, rows} and updates both the
// kernel winsize and the emulator grid.
func (p *LibghosttyPTY) handleResize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Cols == 0 || body.Rows == 0 {
		http.Error(w, "cols and rows must be positive", http.StatusBadRequest)
		return
	}
	if err := p.Resize(body.Cols, body.Rows); err != nil {
		http.Error(w, "resize: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
