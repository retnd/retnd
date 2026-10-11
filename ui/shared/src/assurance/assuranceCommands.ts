/**
 * The `retnd` command each assurance control is equivalent to, in ONE place
 * (issue #985, checklist section 3: no web-only behaviour).
 *
 * They are kept together because the verbs are the CLI half of this epic's
 * work and are the one thing here that has to be reconciled with it: when
 * the commands land, this file is the only edit that follows. CommandEcho
 * prints exactly what is returned, so nothing on screen can disagree with it.
 *
 * The vocabulary is `recover`, `scrub` and `recovery-test`. The older
 * archival-thaw and snapshot-extract verbs keep their own meanings and are
 * deliberately not reused for any of these.
 */
import type { RecoveryTarget } from "./types";

export function scrubCommand(setId: string): string {
  return "retnd scrub " + setId;
}

export function recoveryTestCommand(setId: string): string {
  return "retnd recovery-test " + setId;
}

export function recoverCommand(artifactId: string, target: RecoveryTarget, where: string, acknowledgeCost: boolean): string {
  const to = target === "browser" ? "--to browser" : target === "staging" ? "--to staging " + (where || "<name>") : "--to destination " + (where || "<id>");
  return "retnd recover " + artifactId + " " + to + (acknowledgeCost ? " --accept-cost" : "");
}

export function cancelOperationCommand(operationId: string): string {
  return "retnd operations cancel " + operationId;
}
