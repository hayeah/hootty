import { TerminalPane as LibTerminalPane } from "@hayeah/supervisor-termui";
import { observer } from "mobx-react-lite";
import type { DataSource } from "../data/source";
import type { SessionSummary } from "../data/types";

interface Props {
  ds: DataSource;
  session: SessionSummary;
}

// TerminalPane renders a single session's PTY in the main column.
// No header: the sidebar card carries all session metadata and the
// close control. Pane is pure terminal, padded in black so the
// ghostty-web canvas has a visible bezel. The outer wrapper owns
// the padding + bg; @hayeah/supervisor-termui's inner canvas host fills the
// space.
//
// Keyed by `session.key` at the child so switching sessions unmounts
// + remounts the library component — fresh Terminal, fresh canvas,
// fresh WS attach per session. The load-bearing init ordering
// (canvas mask + term.reset before stream attach) + mode-aware
// wheel handler live inside @hayeah/supervisor-termui's mountTerminal.
export const TerminalPane = observer(function TerminalPane({ ds, session }: Props) {
  return (
    <div className="flex flex-1 flex-col bg-[#0f1018] p-3">
      <div className="relative flex min-h-0 flex-1 overflow-hidden rounded-sm bg-[#0f1018]">
        <LibTerminalPane
          key={session.key}
          attach={() => ds.attach(session.key)}
          className="flex h-full w-full min-h-0"
        />
      </div>
    </div>
  );
});
