import { observer } from "mobx-react-lite";
import { useState } from "react";
import type { DataSource } from "../data/source";
import { SessionSidebar } from "./SessionSidebar";
import { TerminalPane } from "./TerminalPane";

interface Props {
  ds: DataSource;
}

// SessionWorkspace is the two-pane shell: tabs on the left, the
// active terminal on the right. The component is intentionally
// DataSource-agnostic — Preview passes in a MockDataSource, Live
// will pass in a LiveDataSource backed by fetch + WebSocket.
export const SessionWorkspace = observer(function SessionWorkspace({ ds }: Props) {
  const [activeKey, setActiveKey] = useState<string | null>(() => ds.sessions[0]?.key ?? null);

  const active = ds.sessions.find((s) => s.key === activeKey) ?? null;

  async function closeSession(key: string) {
    await ds.closeSession(key);
    // If we just closed the active tab, focus whatever's still there.
    if (key === activeKey) {
      const next = ds.sessions[0]?.key ?? null;
      setActiveKey(next);
    }
  }

  return (
    <div className="flex h-dvh bg-background text-foreground">
      <SessionSidebar
        sessions={ds.sessions}
        activeKey={activeKey}
        onSelect={setActiveKey}
        onCreate={(cmd) => ds.createSession(cmd)}
        onClose={closeSession}
      />
      <main className="flex min-w-0 flex-1 flex-col">
        {active ? (
          <TerminalPane ds={ds} session={active} />
        ) : (
          <div className="flex flex-1 items-center justify-center text-muted-foreground">
            No sessions. Run{" "}
            <code className="mx-2 rounded bg-muted px-1.5 py-0.5 text-sm">ptydemo run -- bash</code>{" "}
            to start one.
          </div>
        )}
      </main>
    </div>
  );
});
