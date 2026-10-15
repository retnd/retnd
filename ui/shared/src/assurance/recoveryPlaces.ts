/**
 * The two places a recovery can go besides this browser, and the words every
 * screen uses for them (issue #1190). They live in this plain module rather
 * than in RecoveryTargetFields.tsx so the Playwright helpers in e2e can import
 * the same strings without pulling in React, and so the component file only
 * exports components (Fast Refresh needs that).
 *
 * "Staging" says where the root is, not what goes under it: on Recover
 * artifact the name is a file path and on the other screens it's a folder, so
 * each screen's `meaning` line says which. "This retnd host" names the machine
 * retnd runs on, so it can't be mistaken for the host a backup came from.
 */
import type { RecoveryTargetSpec } from "@shared/api/contracts";

/** A recovery target other than a browser download. Derived from the wire
 *  type so a renamed kind fails to compile here. */
export type RecoveryPlace = Exclude<RecoveryTargetSpec["kind"], "download">;

/** Every recovery target a screen can have chosen, a browser download
 *  included. */
export type RecoveryKind = RecoveryTargetSpec["kind"];

/** The words for each choice, the same on every screen. */
export const RECOVERY_PLACE_LABELS: Readonly<Record<RecoveryPlace, string>> = {
  staging: "Under the staging root on this retnd host",
  storage: "Configured destination"
};

/** The drop-down's own name. It differs from the radio's so a screen reader
 *  doesn't hear two controls called "Configured destination" side by side. */
export const DESTINATION_SELECT_NAME = "Which configured destination";
