import type { AttachStream } from "./AttachStream";
import { defaultTheme, type TerminalTheme } from "./defaults";

export interface MountTerminalOptions {
  attach: () => AttachStream;
  theme?: Partial<TerminalTheme>;
}

// mountTerminal lazily imports ghostty-web, initializes the WASM
// runtime once, constructs a Terminal, and wires up the
// bidirectional AttachStream. FitAddon handles container-fit:
// fit() once on mount (sets initial cols/rows + triggers the
// backend resize via the onResize event wired to stream.resize),
// observeResize() keeps it sized as the window changes. Returns a
// disposer that the effect uses on unmount.
//
// Ordering is load-bearing: we fit + clear BEFORE attaching the
// stream, and we mask the canvas immediately after `term.open`.
// Two reasons:
//
//   - Ghostty-web 0.3.0 fires a synchronous first render from
//     inside `term.open()` — before our code gets a chance to run.
//     After many session switches the wasmTerm's freshly-allocated
//     grid sometimes starts with non-zero cells (content bleeding
//     from a prior-disposed emulator in the same shared WASM
//     memory), and those stale cells paint onto the fresh canvas
//     during that first sync render. Overlaying the canvas with a
//     solid fill of the theme background right after open clobbers
//     those pixels before the user can see them.
//   - `term.reset()` frees and recreates the wasmTerm, so the next
//     render frame walks an emulator we *know* is empty. After
//     reset + fit, whatever bytes the server sends on the new
//     attach land on a known-clean grid at the correct final size;
//     the snapshot's cursor-positioned writes don't need to touch
//     every cell because every cell was already cleared.
//
// If either step is skipped, stale content from a prior session
// can survive onto the new session's canvas.
export function mountTerminal(
  host: HTMLElement,
  opts: MountTerminalOptions,
): () => void {
  const theme = { ...defaultTheme, ...(opts.theme ?? {}) };
  let disposed = false;
  const teardown: Array<() => void> = [];

  (async () => {
    // biome-ignore lint/suspicious/noExplicitAny: ghostty-web types
    const { init, Terminal, FitAddon } = (await import("ghostty-web")) as any;
    if (disposed) return;
    await init();
    if (disposed) return;

    const term = new Terminal({
      fontSize: theme.fontSize,
      theme: {
        background: theme.background,
        foreground: theme.foreground,
      },
    });
    term.open(host);
    teardown.push(() => term.dispose?.());

    // Mask pixels laid down by the synchronous first render in
    // `term.open()` (see the block comment above mountTerminal).
    const canvas = host.querySelector("canvas");
    if (canvas instanceof HTMLCanvasElement) {
      const ctx = canvas.getContext("2d");
      if (ctx) {
        ctx.fillStyle = theme.background;
        ctx.fillRect(0, 0, canvas.width, canvas.height);
      }
    }

    // Fit BEFORE reset so reset's fresh wasmTerm is allocated at
    // the final cols/rows — avoids later `wasmTerm.resize` from
    // 80×24 to (say) 200×44 leaving the extended cells in whatever
    // state the shared-WASM allocator happened to return.
    const fit = new FitAddon();
    term.loadAddon(fit);
    teardown.push(() => fit.dispose?.());
    try {
      fit.fit();
    } catch {
      // If fit fails (container not sized yet), observeResize
      // will catch it on the next frame.
    }
    fit.observeResize();

    // Now recreate the wasmTerm at the final dimensions so every
    // subsequent render frame walks a grid we know to be empty.
    term.reset?.();

    const stream = opts.attach();
    teardown.push(() => stream.close());

    // Backend → terminal bytes.
    const unsubscribe = stream.onBytes((bytes) => {
      term.write(bytes);
    });
    teardown.push(unsubscribe);

    // Terminal → backend input (keystrokes, paste).
    const onDataDispose = term.onData?.((data: string) => {
      stream.send(new TextEncoder().encode(data));
    });
    if (onDataDispose) teardown.push(() => onDataDispose.dispose?.());

    // Mode-aware mouse-wheel. On normal screen, the wheel scrolls the
    // emulator's local scrollback (xterm.js-style). On alt screen
    // (vim, less, htop), wheel ticks are forwarded to the PTY as
    // arrow-key escapes so the app scrolls its own buffer. Owning
    // this dispatch here — rather than relying on ghostty-web's
    // built-in — keeps the behaviour testable from our code and
    // decouples us from upstream regressions. Installed per-mount,
    // so each session's fresh Terminal (the keyed-remount in
    // TerminalPane creates one) gets its own handler with the right
    // `stream` closed over.
    const encoder = new TextEncoder();
    term.attachCustomWheelEventHandler?.((e: WheelEvent) => {
      e.preventDefault();
      const isAlt = term.buffer?.active?.type === "alternate";
      if (isAlt) {
        const seq = e.deltaY > 0 ? "\x1b[B" : "\x1b[A";
        const lines = normaliseDeltaToLines(e, term.rows ?? 24);
        const ticks = Math.min(5, Math.max(1, Math.abs(Math.round(lines))));
        const bytes = encoder.encode(seq.repeat(ticks));
        stream.send(bytes);
      } else {
        const lines = normaliseDeltaToLines(e, term.rows ?? 24);
        const rounded = Math.trunc(lines);
        if (rounded !== 0) term.scrollLines?.(rounded);
      }
      return true; // we handled it; ghostty-web should skip its default.
    });

    // Ongoing container-fit events — each fit() fires onResize,
    // which we forward to the remote PTY.
    const onResizeDispose = term.onResize?.(
      ({ cols, rows }: { cols: number; rows: number }) => {
        stream.resize(cols, rows);
      },
    );
    if (onResizeDispose) teardown.push(() => onResizeDispose.dispose?.());

    // Send the initial (post-fit) size to the backend so the
    // snapshot-then-live stream is shaped correctly.
    stream.resize(term.cols, term.rows);
  })().catch((err) => {
    host.innerText = `terminal failed: ${err?.message ?? String(err)}`;
  });

  return () => {
    disposed = true;
    for (const fn of teardown) {
      try {
        fn();
      } catch {
        // best-effort
      }
    }
  };
}

// normaliseDeltaToLines converts a WheelEvent's deltaY into an
// approximate line count, handling the three deltaMode values browsers
// may emit. Pixel deltas are divided by a nominal row height; line
// deltas pass through; page deltas multiply by the viewport row count.
function normaliseDeltaToLines(e: WheelEvent, rows: number): number {
  if (e.deltaMode === WheelEvent.DOM_DELTA_PAGE) return e.deltaY * rows;
  if (e.deltaMode === WheelEvent.DOM_DELTA_LINE) return e.deltaY;
  // DOM_DELTA_PIXEL (0). 22 matches a typical 13-15px monospace line.
  return e.deltaY / 22;
}
