/**
 * Whether the engine's destructive gate is open, read from the engine
 * (issue #1165).
 *
 * Every screen that presses a gated act used to assume the answer. That was
 * true of every shipped build and is not a thing a screen should know: the
 * gate can open when #92 ships a verified one, and a later change can give
 * it a reason of its own (a local-account profile, say). GET
 * /system/capabilities reports it as `destructive_operations`, and this is
 * the one place the shared UI reads it.
 *
 * Three states, not two. `unknown` is what a page has until the answer
 * arrives, and what it keeps if the read fails or the engine is older than
 * this bundle and says nothing. It is never treated as open and never as
 * shut: a screen that cannot tell stays quiet and lets the engine's own
 * refusal, which is always correct, do the talking.
 */
import { useEffect, useState } from "react";

import { useApi } from "@shared/api/ApiContext";

export interface GateReading {
  state: "open" | "shut" | "unknown";
  /** Why it is shut, in the engine's words. Empty unless shut. */
  reason: string;
}

const UNKNOWN: GateReading = { state: "unknown", reason: "" };

export function useDestructiveGate(): GateReading {
  const api = useApi();
  const [reading, setReading] = useState<GateReading>(UNKNOWN);

  useEffect(() => {
    // A client that predates the capability read has nothing to ask.
    if (typeof api.getCapabilities !== "function") return;
    let live = true;
    api.getCapabilities().then(
      (caps) => {
        if (live) setReading({ state: caps.destructiveOperations.state, reason: caps.destructiveOperations.reason });
      },
      // Not knowing is not "open". The refusal, if there is one, is the engine's.
      () => undefined
    );
    return () => {
      live = false;
    };
  }, [api]);

  return reading;
}
