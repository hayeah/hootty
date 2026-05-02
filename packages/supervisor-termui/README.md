# @hayeah/termui

Reusable terminal-UI primitives for PTY-over-WebSocket dashboards.
Ships a React `TerminalPane` component backed by
[ghostty-web](https://www.npmjs.com/package/ghostty-web), a thin
`AttachStream` byte-pipe contract, and a reference WebSocket
transport matching the supervisor library's wire format.

```tsx
import { TerminalPane, WebSocketAttach } from "@hayeah/termui";

function SessionTerminal({ sessionKey }: { sessionKey: string }) {
  return (
    <TerminalPane
      key={sessionKey}
      attach={() =>
        new WebSocketAttach({
          url: `ws://${location.host}/api/sessions/${sessionKey}/attach`,
        })
      }
    />
  );
}
```

## API

- `AttachStream` — interface. Bytes both directions + resize + close.
- `WebSocketAttach` — reference impl against the supervisor's WS
  attach protocol: binary frames carry PTY bytes, text frames
  carry `{"type":"resize","cols":N,"rows":M}`.
- `attach(opts)` — convenience factory equivalent to
  `new WebSocketAttach(opts)`.
- `TerminalPane` — React component. One prop: `attach: () =>
  AttachStream`. Wire a session key via React's `key` for
  fresh-mount on switch.
- `mountTerminal(host, opts)` — lower-level binding if you're not
  using React. Returns a teardown function.
- `defaultTheme`, `defaultFontFamily`, `defaultFontSize` —
  centralized theme constants.

## Peer deps

`react@^19`, `react-dom@^19`, `ghostty-web@^0.3`. ghostty-web
ships a WASM runtime; two copies in the tree don't share state,
so the consumer resolves a single version via peer-dep pinning.

## Build

`pnpm build` (tsup → ESM + d.ts in `dist/`). Run from the workspace
root (`libs/hayeah-go/supervisor/`) via `pnpm -r build` to build
alongside `supervisor-webui`.

## Testing

`pnpm test` — vitest runs unit tests for `WebSocketAttach` against
a mock WebSocket (byte/resize round-trip, coalesce, custom encoder,
close idempotency). React bits are exercised end-to-end via the
consumer apps' `/preview` routes + `__tap__` API.
