/**
 * The sentences for the activity-feed events retained-copy encryption writes
 * (EPIC N, issue #995; core/internal/obs/events.go, #990 and #992).
 *
 * Two families, both durable and both written once per moment, never per
 * chunk: the key's life (`key_created`, `key_rotated`, `key_bundle_exported`,
 * `key_bundle_confirmed`, `key_retired`) and the stages of an encrypted copy
 * (`copy_encrypting`, `copy_uploading_encrypted`, `copy_verifying_stored`,
 * `copy_decrypt_verifying`). The fields they carry are ids and digests and
 * nothing else; none of these sentences can print key material because none of
 * the events can carry it.
 *
 * Byte counters are NOT here: they are live progress (the Encrypting and
 * Decrypt-verifying stages of #1290, drawn by the activity strip and the
 * operation panel), and these lines are the durable record, so a line says that
 * the stage finished and with what result.
 *
 * The colour of a line is the engine's stated result, like every line's; the
 * words follow the same fact and say "refused" or "failed" only when the result
 * does. A correlation id the line carries is appended, so the entry can be
 * matched to the request that caused it.
 */
import type { SetActivityEvent } from "@shared/types/activity";
import { artifactTail } from "@shared/assurance/feedText";

const FAMILY = new Set([
  "key_created", "key_rotated", "key_bundle_exported", "key_bundle_confirmed", "key_retired",
  "copy_encrypting", "copy_uploading_encrypted", "copy_verifying_stored", "copy_decrypt_verifying"
]);

/** Whether this feed event is one this file words. */
export const isEncryptionEvent = (event: string): boolean => FAMILY.has(event);

const outcome = (e: SetActivityEvent, ok: string, bad: string): string =>
  e.result === "error" ? bad : e.result === "warn" ? bad : ok;

/** The sentence for one of the nine events, or null for any other. */
export function encryptionEventText(e: SetActivityEvent): string | null {
  const f = e.fields;
  const key = f.key_id ? " " + f.key_id : "";
  const copy = (f.artifact ? artifactTail(f.artifact) : "a copy") + (f.medium ? " on " + f.medium : "");
  let text: string;
  switch (e.event) {
    case "key_created":
      text = outcome(e, "managed key created:" + key, "managed key was not created:" + key);
      break;
    case "key_rotated":
      text = outcome(
        e,
        "managed key rotated: new copies use" + key + (f.previous_key_id ? ", " + f.previous_key_id + " stays readable for the copies that record it" : ""),
        "managed key was not rotated"
      );
      break;
    case "key_bundle_exported":
      text = outcome(e, "recovery bundle exported for key" + key + (f.digest ? ", digest " + f.digest : ""), "recovery bundle export for key" + key + " failed");
      break;
    case "key_bundle_confirmed":
      text = outcome(e, "recovery bundle confirmed stored for key" + key + " (a statement by a person)", "recovery bundle was not confirmed for key" + key);
      break;
    case "key_retired":
      text = outcome(e, "managed key retired:" + key, "managed key was not retired:" + key + " (copies still record it, or it is the active key)");
      break;
    case "copy_encrypting":
      text = outcome(e, "encrypted " + copy, "encrypting " + copy + " failed, nothing was stored");
      break;
    case "copy_uploading_encrypted":
      text = outcome(e, "uploaded the encrypted copy of " + copy, "uploading the encrypted copy of " + copy + " failed");
      break;
    case "copy_verifying_stored":
      text = outcome(e, "read back the stored bytes of " + copy + " and measured them", "reading back the stored bytes of " + copy + " failed");
      break;
    case "copy_decrypt_verifying":
      text = outcome(
        e,
        "decrypt-verified " + copy + (f.key_id ? " under key " + f.key_id : "") + ": plaintext digest matches",
        "decrypt-verify of " + copy + (f.key_id ? " under key " + f.key_id : "") + " did not match or could not complete, nothing was written"
      );
      break;
    default:
      return null;
  }
  return f.correlation_id ? text + "\n  correlation id " + f.correlation_id : text;
}
