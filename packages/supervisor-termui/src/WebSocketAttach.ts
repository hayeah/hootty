import type { AttachStream } from "./AttachStream";

export interface AttachOptions {
  // Full ws:// or wss:// URL to the attach endpoint. The caller
  // builds this; the package is session-naming-agnostic.
  url: string;

  // Text side-channel encoder for resize messages. Defaults to the
  // supervisor's envelope (JSON {type:"resize", cols, rows}).
  // Override if the backend expects a different shape.
  encodeResize?: (cols: number, rows: number) => string;
}

function defaultEncodeResize(cols: number, rows: number): string {
  return JSON.stringify({ type: "resize", cols, rows });
}

// WebSocketAttach is the reference AttachStream implementation. It
// wraps a WebSocket whose protocol is:
//
//   binary frames (server → client) = PTY bytes
//   binary frames (client → server) = user input (keystrokes, paste)
//   text frames   (client → server) = JSON control, today only
//                                     {type:"resize", cols, rows}
//
// The text-frame envelope is configurable via `encodeResize` for
// backends that want a different shape. Bytes-both-ways is
// structural and not expected to change.
export class WebSocketAttach implements AttachStream {
  private listeners = new Set<(bytes: Uint8Array) => void>();
  private ws: WebSocket;
  private closed = false;
  private queuedResize: { cols: number; rows: number } | null = null;
  private encodeResize: (cols: number, rows: number) => string;

  constructor(opts: AttachOptions) {
    this.encodeResize = opts.encodeResize ?? defaultEncodeResize;
    this.ws = new WebSocket(opts.url);
    this.ws.binaryType = "arraybuffer";

    this.ws.addEventListener("open", () => {
      if (this.queuedResize) {
        this.sendResizeFrame(this.queuedResize.cols, this.queuedResize.rows);
        this.queuedResize = null;
      }
    });

    this.ws.addEventListener("message", (ev) => {
      if (this.closed) return;
      if (typeof ev.data === "string") {
        // Today the server never sends text frames; reserved for
        // future {type:"state"} or {type:"exited"} envelopes.
        return;
      }
      if (ev.data instanceof ArrayBuffer) {
        const bytes = new Uint8Array(ev.data);
        for (const fn of this.listeners) fn(bytes);
      }
    });

    this.ws.addEventListener("close", () => {
      this.closed = true;
    });
  }

  onBytes(fn: (bytes: Uint8Array) => void): () => void {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  send(bytes: Uint8Array): void {
    if (this.closed || this.ws.readyState !== WebSocket.OPEN) return;
    this.ws.send(bytes);
  }

  resize(cols: number, rows: number): void {
    if (this.closed) return;
    if (this.ws.readyState !== WebSocket.OPEN) {
      // Coalesce the last pending resize — no point sending the
      // interim sizes from a burst that fired before open.
      this.queuedResize = { cols, rows };
      return;
    }
    this.sendResizeFrame(cols, rows);
  }

  private sendResizeFrame(cols: number, rows: number) {
    this.ws.send(this.encodeResize(cols, rows));
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.listeners.clear();
    try {
      this.ws.close();
    } catch {
      // ignore
    }
  }
}

// attach is a convenience factory for callers who prefer a
// function over `new WebSocketAttach(...)`. Both produce the same
// instance.
export function attach(opts: AttachOptions): AttachStream {
  return new WebSocketAttach(opts);
}
