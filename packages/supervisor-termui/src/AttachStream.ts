// AttachStream is a bidirectional byte pipe. One per terminal
// attach. It's the thin contract ghostty-web needs: bytes flow
// both directions, plus a resize signal toward the backend.
//
// The default implementation (WebSocketAttach) speaks the
// supervisor's wire format (binary WS frames = PTY bytes,
// {type:"resize"} JSON text frames), but anything that conforms
// to this interface plugs in — mock streams for tests, alternative
// transports, etc.
export interface AttachStream {
  // onBytes is invoked as the backend produces output. Delivered
  // in chunks; consumer doesn't need to re-parse boundaries.
  // Returns an unsubscribe function.
  onBytes(fn: (bytes: Uint8Array) => void): () => void;

  // send writes user input to the backend PTY master.
  send(bytes: Uint8Array): void;

  // resize informs the backend of a new terminal size.
  resize(cols: number, rows: number): void;

  // close tears down the stream. Safe to call multiple times.
  close(): void;
}
