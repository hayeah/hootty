import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { WebSocketAttach } from "./WebSocketAttach";

// FakeWebSocket mimics enough of the browser WebSocket API for
// WebSocketAttach's purposes: open/close/message events, binary
// + text send, readyState. It exposes test helpers to drive the
// "server side" — dispatch a binary frame to the client,
// capture frames the client sent.
class FakeWebSocket implements EventTarget {
  static CONNECTING = 0 as const;
  static OPEN = 1 as const;
  static CLOSING = 2 as const;
  static CLOSED = 3 as const;
  readyState: number = FakeWebSocket.CONNECTING;
  binaryType: "arraybuffer" | "blob" = "blob";
  url: string;
  sent: Array<ArrayBuffer | string> = [];

  private listeners: Record<string, Set<(ev: Event) => void>> = {
    open: new Set(),
    message: new Set(),
    close: new Set(),
  };

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, fn: (ev: Event) => void) {
    (this.listeners[type] ??= new Set()).add(fn);
  }
  removeEventListener(type: string, fn: (ev: Event) => void) {
    this.listeners[type]?.delete(fn);
  }
  dispatchEvent(ev: Event): boolean {
    const set = this.listeners[ev.type];
    if (set) for (const fn of set) fn(ev);
    return true;
  }

  send(data: ArrayBuffer | ArrayBufferView | string): void {
    if (data instanceof ArrayBuffer) this.sent.push(data);
    else if (typeof data === "string") this.sent.push(data);
    else this.sent.push(data.buffer.slice(0) as ArrayBuffer);
  }

  close(): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }

  // test helpers
  _open(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.dispatchEvent(new Event("open"));
  }
  _deliverBinary(bytes: Uint8Array): void {
    const buf = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
    this.dispatchEvent(
      Object.assign(new Event("message"), { data: buf }) as MessageEvent,
    );
  }
}

let originalWebSocket: typeof WebSocket | undefined;
let fakes: FakeWebSocket[] = [];

class WebSocketShim extends FakeWebSocket {
  constructor(url: string) {
    super(url);
    fakes.push(this);
  }
  static OPEN = FakeWebSocket.OPEN;
  static CONNECTING = FakeWebSocket.CONNECTING;
  static CLOSING = FakeWebSocket.CLOSING;
  static CLOSED = FakeWebSocket.CLOSED;
}

beforeEach(() => {
  originalWebSocket = globalThis.WebSocket;
  fakes = [];
  // biome-ignore lint/suspicious/noExplicitAny: test shim
  (globalThis as any).WebSocket = WebSocketShim;
});

afterEach(() => {
  globalThis.WebSocket = originalWebSocket as typeof WebSocket;
});

describe("WebSocketAttach", () => {
  it("delivers server → client bytes to onBytes listeners", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const received: Uint8Array[] = [];
    attach.onBytes((b) => received.push(b));

    const ws = fakes[0];
    ws._open();
    ws._deliverBinary(new Uint8Array([104, 105])); // "hi"

    expect(received).toHaveLength(1);
    expect(Array.from(received[0])).toEqual([104, 105]);
  });

  it("sends client → server input bytes as binary frames", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const ws = fakes[0];
    ws._open();

    attach.send(new Uint8Array([65, 66])); // "AB"

    expect(ws.sent).toHaveLength(1);
    const buf = ws.sent[0] as ArrayBuffer;
    expect(Array.from(new Uint8Array(buf))).toEqual([65, 66]);
  });

  it("drops sends while the socket is not OPEN", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const ws = fakes[0];
    // still CONNECTING
    attach.send(new Uint8Array([1, 2]));
    expect(ws.sent).toHaveLength(0);
  });

  it("sends resize as a JSON text frame", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const ws = fakes[0];
    ws._open();

    attach.resize(120, 40);

    expect(ws.sent).toHaveLength(1);
    expect(ws.sent[0]).toBe(JSON.stringify({ type: "resize", cols: 120, rows: 40 }));
  });

  it("coalesces resizes fired before open, flushes on open", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const ws = fakes[0];
    // fire multiple before open — only the latest should survive
    attach.resize(80, 24);
    attach.resize(120, 40);
    expect(ws.sent).toHaveLength(0);

    ws._open();
    expect(ws.sent).toHaveLength(1);
    expect(ws.sent[0]).toBe(JSON.stringify({ type: "resize", cols: 120, rows: 40 }));
  });

  it("honors a custom encodeResize", () => {
    const attach = new WebSocketAttach({
      url: "ws://x/attach",
      encodeResize: (cols, rows) => `R ${cols}x${rows}`,
    });
    const ws = fakes[0];
    ws._open();

    attach.resize(120, 40);
    expect(ws.sent[0]).toBe("R 120x40");
  });

  it("close is idempotent and stops delivering bytes", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const received: Uint8Array[] = [];
    attach.onBytes((b) => received.push(b));

    const ws = fakes[0];
    ws._open();
    attach.close();
    attach.close(); // idempotent

    ws._deliverBinary(new Uint8Array([9, 9]));
    expect(received).toHaveLength(0);
  });

  it("onBytes returns an unsubscribe function", () => {
    const attach = new WebSocketAttach({ url: "ws://x/attach" });
    const received: Uint8Array[] = [];
    const off = attach.onBytes((b) => received.push(b));
    const ws = fakes[0];
    ws._open();

    ws._deliverBinary(new Uint8Array([1]));
    off();
    ws._deliverBinary(new Uint8Array([2]));

    expect(received).toHaveLength(1);
    expect(Array.from(received[0])).toEqual([1]);
  });
});
