/**
 * One backup set's workflow block, read once and written through one
 * function, for the set page and its Workflow card to share (#1189).
 *
 * The page owns this because its edit session has to: the before and after
 * directory boxes are part of what SAVE ALL saves and what the CANCEL ledger
 * lists, so the page needs the saved values and the write. The card gets the
 * same object, so a write from either side reloads the one copy both of them
 * draw, and the save gate's refusal lands in the card's banner whichever
 * control sent it.
 */
import { useCallback, useState } from "react";

import { useApi } from "@shared/api/ApiContext";
import { apiErrorOf, describeFailure, workflowScriptRefusalOf } from "@shared/api/failure";
import type {
  BackupSetWorkflow,
  BackupSetWorkflowPatch,
  WorkflowBlockingScript
} from "@shared/api/contracts";
import { useAsync } from "@shared/hooks/useAsync";
import type { AsyncState } from "@shared/hooks/useAsync";

/** The two directory boxes, named as the PATCH body names them. */
export type HookDirKey = "beforeDir" | "afterDir";
export const HOOK_DIR_KEYS: readonly HookDirKey[] = ["beforeDir", "afterDir"];
export const HOOK_DIR_LABEL: Record<HookDirKey, string> = {
  beforeDir: "Before workflow directory",
  afterDir: "After workflow directory"
};

export interface SetWorkflowModel {
  workflow: AsyncState<BackupSetWorkflow>;
  /** The last write the save gate refused, kept until the next write: a
   *  banner that vanished on the next render would leave an operator with a
   *  control that snapped back and no reason why. */
  refusal: { blocking: WorkflowBlockingScript[]; message: string } | null;
  /** One workflow write. Re-throws on failure, because the control that
   *  sent it has its own "saving" state to unwind and its own sentence. */
  write(patch: BackupSetWorkflowPatch): Promise<BackupSetWorkflow>;
}

export function useSetWorkflow(source: string, set: string): SetWorkflowModel {
  const api = useApi();
  const workflow = useAsync(() => api.getBackupSetWorkflow(source, set), [api, source, set]);
  const [refusal, setRefusal] = useState<SetWorkflowModel["refusal"]>(null);
  const reload = workflow.reload;
  const write = useCallback(
    (patch: BackupSetWorkflowPatch) =>
      api
        .patchBackupSetWorkflow(source, set, patch)
        .then((result) => {
          setRefusal(null);
          reload();
          return result;
        })
        .catch((e: unknown) => {
          const failure = describeFailure(e, "this workflow configuration was not saved");
          setRefusal(
            apiErrorOf(e)?.code === "WORKFLOW_SCRIPT_REJECTED"
              ? { blocking: workflowScriptRefusalOf(e), message: failure.message }
              : null
          );
          throw e;
        }),
    // `reload` is a fresh closure per render, but it only ever bumps a
    // counter, so the newest one and an older one do the same thing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, source, set]
  );
  return { workflow, refusal, write };
}

/**
 * Where a directory box stands against what is saved.
 *
 * The text is trimmed before it is compared, because it is trimmed before
 * it is sent: a box that only gained spaces would otherwise read as a
 * change and go out as the value it already had, or, around an empty box,
 * as "" (#1192 review).
 *
 * "emptied" is its own answer because an empty string CLEARS the
 * directory, and clearing turns a stage of hooks off. That is only ever
 * done by the box's Clear button, never by a Save or by SAVE ALL.
 */
export function hookDirState(
  draft: string | undefined,
  saved: string
): "clean" | "changed" | "emptied" {
  if (draft === undefined) return "clean";
  const typed = draft.trim();
  if (typed === saved) return "clean";
  return typed === "" ? "emptied" : "changed";
}

/** The sentence under a box whose write failed, or null when the save
 *  gate refused it: that refusal names its scripts, and the card's banner
 *  draws it in full. */
export function workflowSaveFailureText(err: unknown, fallback: string): string | null {
  return apiErrorOf(err)?.code === "WORKFLOW_SCRIPT_REJECTED"
    ? null
    : describeFailure(err, fallback).message;
}
