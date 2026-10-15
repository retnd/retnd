/**
 * Starts and follows one set's recovery test from a button that lives
 * somewhere other than the set's Assurance section (issues #1174 and #1178).
 *
 * The Dashboard tile's rows and the set page's Validation pane both press
 * the control the Assurance section already has: POST
 * /backup-sets/{source}/{set}/recovery-test, through the same seam
 * (`AssuranceApi.startRecoveryTest`), followed on the same durable feed
 * (`useOperation`). Nothing here starts anything of its own. When the run
 * ends it calls `noteAssuranceChanged()`, which is how every surface that
 * shows a claim (the tile's counts, the Validation pane, the chips) re-reads
 * the verification model together.
 *
 * A recovery test is not a destructive-gate act in the engine, so the gate
 * does not disable the button. If a build ever does refuse one with the
 * gate's code, the refusal is said the way every other gated screen says it
 * (the engine's reason, nothing changed, the command for the host).
 */
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { gateRefusalNotice, isGateRefusal } from "@shared/api/gate";
import type { GateRefusalNotice } from "@shared/api/gate";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { recoveryTestCommand } from "./assuranceCommands";
import { noteAssuranceChanged } from "./verificationModel";
import { useOperation } from "./useOperation";

export function useRunAssurance(setId: string, opts: { attach?: boolean } = {}) {
  const root = useApi();
  const api = root.assurance;
  const gate = useDestructiveGate();
  const offered = !!api?.offers.recoveryTest;
  const [opId, setOpId] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refused, setRefused] = useState<GateRefusalNotice | null>(null);
  const [joined, setJoined] = useState(false);
  const op = useOperation(offered ? api : undefined, opId, () => noteAssuranceChanged());

  // A page opened while a run is going attaches to it instead of offering a
  // second press (the service would join it anyway, and say so).
  const attach = opts.attach === true;
  useEffect(() => {
    if (!attach || !api || !offered) return;
    let live = true;
    api.activeOperation(setId).then((running) => { if (live && running) setOpId(running.id); }, () => undefined);
    return () => { live = false; };
  }, [attach, api, offered, setId]);

  const run = async () => {
    if (!api || !offered || starting) return;
    setError(null);
    setRefused(null);
    setStarting(true);
    try {
      const started = await api.startRecoveryTest(setId);
      setJoined(!!started.joined);
      setOpId(started.id);
    } catch (e) {
      if (isGateRefusal(e)) {
        setRefused(gateRefusalNotice({
          outcome: "The recovery test was not started",
          nothingChanged: "Nothing was read or written.",
          command: recoveryTestCommand(setId),
          reason: gate.reason,
          correlationId: apiErrorOf(e)?.correlationId
        }));
      } else {
        setError(describeFailure(e, "could not start").message);
      }
    } finally {
      setStarting(false);
    }
  };

  const status = op.operation?.state;
  const running = starting || status === "running";
  return {
    /** The server offers the route; false means show the terminal command only. */
    offered,
    run,
    running,
    starting,
    /** running, complete, failed, cancelled, untracked, or idle before any run. */
    state: (starting ? "running" : status ?? "idle") as "idle" | "running" | "complete" | "failed" | "cancelled" | "untracked",
    operation: op.operation,
    events: op.events,
    feedError: op.error,
    cancel: op.cancel,
    cancelError: op.cancelError,
    joined,
    error,
    refused
  };
}
