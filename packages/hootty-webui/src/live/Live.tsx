import { useEffect, useMemo } from "react";
import { LiveDataSource } from "../data/live";
import { SessionWorkspace } from "../views/SessionWorkspace";

// Live mounts SessionWorkspace against a LiveDataSource wired to
// ptydemo serve's /api/* endpoints. Same component surface as
// /preview — only the data source differs.
export function Live() {
  const ds = useMemo(() => new LiveDataSource(), []);

  useEffect(() => {
    // Hook for browser-driven e2e tests (or a quick eval in
    // devtools). Matches Preview's __tap__ shape so the same
    // test harness works against live and mock.
    // biome-ignore lint/suspicious/noExplicitAny: test harness hook
    (window as any).__tap__ = {
      ds,
      get sessions() {
        return ds.sessions;
      },
      refresh() {
        return ds.refresh();
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
      ds.dispose();
    };
  }, [ds]);

  return <SessionWorkspace ds={ds} />;
}
