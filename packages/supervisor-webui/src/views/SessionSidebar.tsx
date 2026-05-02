import { observer } from "mobx-react-lite";
import { useState } from "react";
import type { SessionSummary } from "../data/types";

interface Props {
  sessions: SessionSummary[];
  activeKey: string | null;
  onSelect(key: string): void;
  onCreate?(cmd: string): Promise<SessionSummary>;
  onClose?(key: string): Promise<void>;
}

// SessionSidebar is the left column: one tab per known session.
// Click a tab to focus its terminal in the main pane. Tabs show
// liveness + state at a glance.
export const SessionSidebar = observer(function SessionSidebar({
  sessions,
  activeKey,
  onSelect,
  onCreate,
  onClose,
}: Props) {
  return (
    <aside className="flex w-72 shrink-0 flex-col border-r border-border bg-card">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <div className="text-sm font-semibold">Sessions</div>
        <div className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
          {sessions.length}
        </div>
      </div>
      {onCreate && <NewSessionForm onCreate={onCreate} onCreated={(s) => onSelect(s.key)} />}
      <nav className="flex-1 overflow-y-auto p-2">
        {sessions.length === 0 && (
          <div className="px-2 py-4 text-xs text-muted-foreground">No sessions yet.</div>
        )}
        {sessions.map((s) => (
          <SessionTab
            key={s.key}
            session={s}
            active={s.key === activeKey}
            onClick={() => onSelect(s.key)}
            onClose={onClose ? () => onClose(s.key) : undefined}
          />
        ))}
      </nav>
      <footer className="border-t border-border px-4 py-2 text-[10px] uppercase tracking-wide text-muted-foreground">
        ptydemo
      </footer>
    </aside>
  );
});

function NewSessionForm({
  onCreate,
  onCreated,
}: {
  onCreate(cmd: string): Promise<SessionSummary>;
  onCreated(s: SessionSummary): void;
}) {
  const [cmd, setCmd] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const s = await onCreate(cmd || "bash -l");
      setCmd("");
      onCreated(s);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={submit}
      className="flex flex-col gap-1 border-b border-border bg-muted/30 px-3 py-2"
    >
      <div className="flex gap-1">
        <input
          type="text"
          value={cmd}
          onChange={(e) => setCmd(e.target.value)}
          placeholder="bash -l"
          disabled={busy}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-2 py-1 font-mono text-xs focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary/40 disabled:opacity-50"
          aria-label="Command to run"
        />
        <button
          type="submit"
          disabled={busy}
          className="shrink-0 rounded-md bg-primary px-2 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
        >
          {busy ? "…" : "+"}
        </button>
      </div>
      {error && <div className="text-[10px] text-destructive">{error}</div>}
    </form>
  );
}

function SessionTab({
  session,
  active,
  onClick,
  onClose,
}: {
  session: SessionSummary;
  active: boolean;
  onClick: () => void;
  onClose?: () => Promise<void> | void;
}) {
  const dotTone =
    session.state === "running"
      ? "bg-green-500"
      : session.state === "starting"
        ? "bg-amber-500"
        : session.state === "exited"
          ? "bg-muted-foreground/40"
          : "bg-muted-foreground/60";

  const badgeTone =
    session.state === "running"
      ? "text-green-500 ring-green-500/30"
      : session.state === "starting"
        ? "text-amber-500 ring-amber-500/30"
        : session.state === "exited"
          ? "text-muted-foreground ring-muted-foreground/30"
          : "text-muted-foreground ring-muted-foreground/30";

  // div-with-role so the close button isn't nested in an outer
  // button (invalid HTML + a11y trap).
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onClick();
        }
      }}
      className={[
        "group relative mb-1 flex w-full cursor-pointer flex-col gap-1 rounded-md border px-3 py-2 text-left transition-colors",
        active
          ? "border-primary/30 bg-primary/5 text-foreground"
          : "border-transparent text-foreground/80 hover:border-border hover:bg-muted",
      ].join(" ")}
    >
      <div className="flex items-center gap-2">
        <span className={`h-2 w-2 shrink-0 rounded-full ${dotTone}`} />
        <span className="min-w-0 flex-1 truncate text-sm font-medium">{session.key}</span>
        <span
          className={`shrink-0 rounded-full px-1.5 py-0.5 text-[9px] font-medium uppercase tracking-wide ring-1 ${badgeTone}`}
        >
          {session.state}
        </span>
        {onClose && (
          <button
            type="button"
            aria-label={`Close session ${session.key}`}
            title="Close session"
            onClick={(e) => {
              e.stopPropagation();
              void onClose();
            }}
            className={[
              "-mr-1 shrink-0 rounded p-1 text-muted-foreground transition-opacity hover:bg-muted-foreground/10 hover:text-foreground focus:opacity-100",
              active ? "opacity-60" : "opacity-0 group-hover:opacity-100",
            ].join(" ")}
          >
            <svg
              width="12"
              height="12"
              viewBox="0 0 12 12"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
              aria-hidden="true"
            >
              <path
                d="M2.5 2.5L9.5 9.5M9.5 2.5L2.5 9.5"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
              />
            </svg>
          </button>
        )}
      </div>
      <div className="truncate pl-4 font-mono text-xs text-muted-foreground">{session.cmd}</div>
      <div className="flex gap-2 pl-4 text-[10px] text-muted-foreground/80">
        {session.pid != null && <span>pid {session.pid}</span>}
        {session.pid != null && <span>·</span>}
        <span>{timeAgo(session.startedAt)}</span>
      </div>
    </div>
  );
}

function timeAgo(iso: string): string {
  const delta = Date.now() - Date.parse(iso);
  const mins = Math.max(0, Math.round(delta / 60_000));
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}
