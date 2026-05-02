import { useEffect, useRef } from "react";
import type { AttachStream } from "./AttachStream";
import { mountTerminal } from "./mountTerminal";
import type { TerminalTheme } from "./defaults";

export interface TerminalPaneProps {
  // Called once per mount to open an AttachStream. Keyed remount
  // on session change is the caller's responsibility — pass a
  // React `key` on <TerminalPane> whenever you want a fresh
  // Terminal + WS attach.
  attach: () => AttachStream;

  theme?: Partial<TerminalTheme>;

  // Wrapper className. The inner canvas host is always h-full
  // w-full min-h-0 so the ghostty-web canvas can size to its
  // container.
  className?: string;
}

// TerminalPane renders a single PTY into a canvas host. It's a
// thin React wrapper over mountTerminal — the effect runs once on
// mount (deps are the stable identity values props.attach + props.theme),
// so parent re-renders don't retear the Terminal down. Caller
// owns keyed-remount behavior via React's `key` prop.
export function TerminalPane({ attach, theme, className }: TerminalPaneProps) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!ref.current) return;
    return mountTerminal(ref.current, { attach, theme });
  }, [attach, theme]);

  return (
    <div className={className ?? "flex h-full w-full min-h-0"}>
      <div ref={ref} className="h-full w-full min-h-0" />
    </div>
  );
}
