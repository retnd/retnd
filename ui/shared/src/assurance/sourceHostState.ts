/**
 * The logic half of the "Source host (SFTP)" destination (issue #1191): what
 * the engine says about one set's source host, and what a dialog may do with
 * it. SourceHost.tsx draws it; the rationale is written there.
 */
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import type { RecoverySourcePreflight } from "@shared/api/contracts";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import type { GateReading } from "@shared/hooks/useDestructiveGate";

export const SOURCE_LABEL = "Source host (SFTP)";

export interface SourceHost {
  /** False while the host is being asked. */
  ready: boolean;
  /** Whether the engine offers the source-host destination at all. An engine
   *  older than #1191 does not, and the dialogs then behave as they did. */
  offered: boolean;
  preflight: RecoverySourcePreflight | null;
  /** Why the source host cannot be the destination here, or null. */
  unavailable: string | null;
  gate: GateReading;
  /** Whether it should start out selected: available, not refused by a shut
   *  gate. A read-only account is still preselected, with its warning, so the
   *  operator sees why it will not work instead of not seeing it at all. */
  preselect: boolean;
}

/** Asks the engine about one set's source host. `setId` null means the
 *  backups in question come from several sets (`spread` of them). */
export function useSourceHost(setId: string | null, spread = 0): SourceHost {
  const api = useApi();
  const gate = useDestructiveGate();
  const offered = typeof api.preflightRecoverySource === "function";
  const [answer, setAnswer] = useState<{ id: string; preflight: RecoverySourcePreflight | null; failure: string | null } | null>(null);

  useEffect(() => {
    if (!offered || !setId) return;
    let live = true;
    const [source, ...rest] = setId.split("/");
    api.preflightRecoverySource(source, rest.join("/")).then(
      (p) => { if (live) setAnswer({ id: setId, preflight: p, failure: null }); },
      (e) => { if (live) setAnswer({ id: setId, preflight: null, failure: describeFailure(e, "The host could not say where its source is.").message }); }
    );
    return () => { live = false; };
  }, [api, offered, setId]);

  const current = answer && answer.id === setId ? answer : null;
  const preflight = current?.preflight ?? null;
  let unavailable: string | null = null;
  if (offered && !setId && spread > 1) {
    unavailable = "These backups come from " + spread + " backup sets, and each set has its own source host, so one upload cannot send them all home. Tick the backups of one set to send them back.";
  } else if (current?.failure) {
    unavailable = current.failure;
  } else if (preflight && !preflight.available) {
    unavailable = "No source host for this set: " + (preflight.reason ?? "the engine did not say why") + ".";
  } else if (preflight && gate.state === "shut") {
    unavailable = "The web interface will not write onto the source host here: " + (gate.reason || "the destructive gate is shut on this deployment") + ". Pick another destination, or run retnd recover on the host.";
  }
  const ready = !offered || (!setId ? true : current !== null);
  return {
    ready, offered, preflight, unavailable, gate,
    preselect: offered && !!preflight?.available && unavailable === null
  };
}

/** Whether a source-host recovery may be asked for with these answers. */
export function sourceHostReady(host: SourceHost, acknowledged: boolean, password: string): boolean {
  const p = host.preflight;
  return !!p?.available && host.unavailable === null && p.writable !== "no" && acknowledged && (!host.gate.stepUpRequired || password !== "");
}

/** The engine's refusals of a source-host request, worded for the dialog. */
export function sourceHostRefusal(code: string | undefined): string | null {
  switch (code) {
    case "STEP_UP_FAILED":
      return "That password was not accepted. Nothing was written to the source host. Check it and try again.";
    case "STEP_UP_REQUIRED":
      return "Enter your password to write onto the source host.";
    case "ACKNOWLEDGEMENT_REQUIRED":
    case "RECOVERY_SOURCE_NOT_ACKNOWLEDGED":
      return "Tick the box to say you understand this writes onto the source host.";
    default:
      return null;
  }
}

