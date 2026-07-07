// React binding over the framework-agnostic wait driver (wait-client.ts).
//
// Owns the consent page's lifecycle: parse the fragment, open the stream, track
// the snapshot + connection health + terminal outcome, and — on `verified` —
// finalize and hand off to the returned redirect. Everything long-lived is torn
// down via one AbortController on unmount.

import * as React from "react";
import {
  cancel as cancelReq,
  driveWaitStream,
  finalize as finalizeReq,
  type Terminal,
} from "./wait-client";
import { parseBrowserCode, type PendingSnapshot } from "./protocol";

export type WaitPhase =
  | "loading" // fragment read, stream opening, no snapshot yet
  | "pending" // snapshot in hand, waiting for the WhatsApp message
  | "reconnecting" // dropped mid-flight, retrying (snapshot still shown)
  | "finalizing" // verified → POSTing finalize
  | "verified" // finalize done, navigating away
  | "denied"
  | "expired"
  | "not_found" // invalid/expired browser code (stream 404) or missing fragment
  | "error"; // finalize failed

export interface WaitState {
  phase: WaitPhase;
  snapshot: PendingSnapshot | null;
  /** Cancel ("This isn't me") — flips to denied locally after the POST. */
  cancel: () => void;
  cancelling: boolean;
}

export function useWait(): WaitState {
  const [phase, setPhase] = React.useState<WaitPhase>("loading");
  const [snapshot, setSnapshot] = React.useState<PendingSnapshot | null>(null);
  const [cancelling, setCancelling] = React.useState(false);
  const codeRef = React.useRef<string | null>(null);

  React.useEffect(() => {
    const code = parseBrowserCode(
      typeof window !== "undefined" ? window.location.hash : "",
    );
    codeRef.current = code;
    if (!code) {
      setPhase("not_found");
      return;
    }

    const ctrl = new AbortController();

    void driveWaitStream({
      browserCode: code,
      signal: ctrl.signal,
      onSnapshot: (snap) => {
        setSnapshot(snap);
        setPhase((p) => (p === "loading" || p === "reconnecting" ? "pending" : p));
      },
      onLive: () => {
        setPhase((p) => (p === "reconnecting" ? "pending" : p));
      },
      onReconnecting: () => {
        setPhase((p) => (p === "pending" || p === "loading" ? "reconnecting" : p));
      },
      onNotFound: () => setPhase("not_found"),
      onTerminal: (t: Terminal) => {
        if (t === "verified") {
          setPhase("finalizing");
          void finalizeReq(code, ctrl.signal)
            .then((res) => {
              if (ctrl.signal.aborted) return;
              setPhase("verified");
              window.location.replace(res.redirect);
            })
            .catch(() => {
              if (!ctrl.signal.aborted) setPhase("error");
            });
        } else {
          setPhase(t); // "denied" | "expired"
        }
      },
    });

    return () => ctrl.abort();
  }, []);

  const cancel = React.useCallback(() => {
    const code = codeRef.current;
    if (!code || cancelling) return;
    setCancelling(true);
    void cancelReq(code).finally(() => {
      setCancelling(false);
      setPhase("denied");
    });
  }, [cancelling]);

  return { phase, snapshot, cancel, cancelling };
}
