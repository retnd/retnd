/**
 * Starts and follows a recovery test from a button that lives somewhere other
 * than the set's Assurance section (issues #1174 and #1178).
 *
 * The Dashboard tile's rows and the set page's Validation pane both press
 * the control the Assurance section already has: POST
 * /backup-sets/{source}/{set}/recovery-test, through the same seam
 * (`AssuranceApi.startRecoveryTest`). A row names its own backup
 * (`artifact_id`), the same as `retnd recovery-test SET --artifact ID`; the
 * pane names none, so the engine tests the set's newest known-good copy. When
 * a run ends the caller's follower calls `noteAssuranceChanged()`, which is
 * how every surface that shows a claim (the tile's counts, the Validation
 * pane, the chips) re-reads the verification model together.
 *
 * A recovery test is not a destructive-gate act in the engine, so the gate
 * does not disable the button. If a build ever does refuse one with the
 * gate's code, the refusal is said the way every other gated screen says it
 * (the engine's reason, nothing changed, the command for the host). The gate
 * reading is the caller's, so a list of rows reads the capabilities once.
 */
import { useEffect, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { gateRefusalNotice, isGateRefusal } from "@shared/api/gate";
import type { GateRefusalNotice } from "@shared/api/gate";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import type { GateReading } from "@shared/hooks/useDestructiveGate";
import { recoveryTestCommand } from "./assuranceCommands";
import type { AssuranceApi, Operation } from "./types";
import { noteAssuranceChanged } from "./verificationModel";
import { useOperation } from "./useOperation";

/** Where a run stands: before any, or the operation's own state. Derived from
 *  the Operation type, so a new state there fails to compile here rather than
 *  quietly blanking a row. */
export type RunState = "idle" | Operation["state"];

export interface StartOutcome {
  operation?: Operation;
  error?: string;
  refused?: GateRefusalNotice;
}

/** Starts one recovery test, and turns a refusal into what the screen says. */
export async function startRecoveryRun(
  api: AssuranceApi,
  setId: string,
  artifactId: string | undefined,
  gate: GateReading
): Promise<StartOutcome> {
  try {
    return { operation: await api.startRecoveryTest(setId, artifactId ? { artifactId } : undefined) };
  } catch (e) {
    if (isGateRefusal(e)) {
      return {
        refused: gateRefusalNotice({
          outcome: "The recovery test was not started",
          nothingChanged: "Nothing was read or written.",
          command: recoveryTestCommand(setId) + (artifactId ? " --artifact " + artifactId : ""),
          reason: gate.reason,
          correlationId: apiErrorOf(e)?.correlationId
        })
      };
    }
    return { error: describeFailure(e, "could not start").message };
  }
}

/** One set's run, for a surface with a single button (the Validation pane). */
export function useRunAssurance(setId: string, opts: { attach?: boolean; gate: GateReading }) {
  const root = useApi();
  const api = root.assurance;
  const offered = !!api?.offers.recoveryTest;
  const [opId, setOpId] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  // State is a render late, so two presses in one tick would both see
  // `starting` false; the ref is what stops the second POST.
  const inFlight = useRef(false);
  const [error, setError] = useState<string | null>(null);
  const [refused, setRefused] = useState<GateRefusalNotice | null>(null);
  const [joined, setJoined] = useState(false);
  // The pane points at the set terminal for the lines (#1176), so it keeps
  // none; the Assurance section following the same run shares this poller.
  const op = useOperation(offered ? api : undefined, opId, () => noteAssuranceChanged(), { retainEvents: false });

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
    if (!api || !offered || inFlight.current) return;
    inFlight.current = true;
    setError(null);
    setRefused(null);
    setStarting(true);
    const out = await startRecoveryRun(api, setId, undefined, opts.gate);
    inFlight.current = false;
    setStarting(false);
    if (out.operation) {
      setJoined(!!out.operation.joined);
      setOpId(out.operation.id);
    }
    setError(out.error ?? null);
    setRefused(out.refused ?? null);
  };

  const status = op.operation?.state;
  const state: RunState = starting ? "running" : status ?? "idle";
  return {
    /** The server offers the route; false means show the terminal command only. */
    offered,
    run,
    running: state === "running",
    starting,
    state,
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
