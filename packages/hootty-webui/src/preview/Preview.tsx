import { useEffect, useMemo } from "react";
import { MockDataSource } from "../data/mock";
import { SessionWorkspace } from "../views/SessionWorkspace";

// Preview hosts the shared workspace against a MockDataSource and
// wires the data source's mutators into window.__tap__ so the
// agent can drive state programmatically from `browser eval`.
export function Preview() {
  const ds = useMemo(() => new MockDataSource(), []);

  useEffect(() => {
    // biome-ignore lint/suspicious/noExplicitAny: test harness hook
    (window as any).__tap__ = {
      __DOC__: PREVIEW_TAP_DOC,
      ds,
      get sessions() {
        return ds.sessions;
      },
      setState(key: string, state: "starting" | "running" | "exited") {
        ds.setState(key, state);
      },
      addSession(key: string, cmd: string) {
        ds.addSession({
          key,
          alive: true,
          state: "running",
          cmd,
          startedAt: new Date().toISOString(),
        });
      },
      createSession(cmd: string) {
        return ds.createSession(cmd);
      },
      closeSession(key: string) {
        return ds.closeSession(key);
      },
    };
    return () => {
      // biome-ignore lint/suspicious/noExplicitAny: test harness hook
      (window as any).__tap__ = null;
    };
  }, [ds]);

  return <SessionWorkspace ds={ds} />;
}

const PREVIEW_TAP_DOC = `
# /preview — window.__tap__
- sessions — the mock session summaries (MobX observable)
- ds — the MockDataSource instance
- setState(key, state) — flip a session's state ("starting" | "running" | "exited")
- addSession(key, cmd) — push a new mock session into the sidebar
- createSession(cmd) — same as clicking the "+" in the sidebar form (returns a Promise)
- closeSession(key) — same as clicking the X on a tab / the Close button in the header
`;
