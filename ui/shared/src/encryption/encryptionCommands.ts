/**
 * The `retnd` command each retained-copy encryption control is equivalent to,
 * in ONE place (EPIC N, issue #995; checklist section 3, no web-only behaviour).
 *
 * The spellings are the ones core/cliecho/routes.go prints for the same route
 * (pinned there by encryption_993_test.go) and core/cmd/retnd/main.go accepts.
 * CommandEcho prints exactly what these return, so a line beside a control
 * cannot disagree with the terminal's own echo of the request it made.
 *
 * Two things are never on a line. A key reference's command arguments are not
 * echoed, only a stand-in, because the arguments are the secret's address; and
 * the bundle export names a placeholder file, because the bundle is written to
 * a file and never printed.
 *
 * # The reverse-direction sweep (the record #993 left to this issue)
 *
 * Every mutating control the browser draws for this feature, and the function
 * that echoes it. encryptionCommands.test.ts walks this table against the
 * operations in the generated contract, so a mutating operation with no
 * control row, or a control with no operation, fails there:
 *
 *   control (screen)                          route                                   echo
 *   Enable, managed key (Settings card)       PATCH /encryption/policy                policyPatchCommand
 *                                             POST  /encryption/keys                  keyCreateCommand
 *   Enable, external reference (Settings)     PATCH /encryption/policy                policyPatchCommand
 *   Turn off (Settings card)                  PATCH /encryption/policy                policyPatchCommand
 *   Remove the block (Settings card)          POST  /encryption/policy/reset          policyResetCommand
 *   Rotate key                                POST  /encryption/keys/rotate           keyRotateCommand
 *   Retire                                    POST  /encryption/keys/{id}/retire      keyRetireCommand
 *   Export recovery bundle                    POST  /encryption/keys/{id}/export      keyExportCommand
 *   I have stored the bundle safely           POST  /encryption/keys/{id}/confirm     keyConfirmCommand
 *   Test reference                            POST  /encryption/providers/test        providerTestCommand
 *   Inherit / Off / Required (set card)       PUT   /backup-sets/{s}/{s}/encryption   setEncryptionCommand
 *   Follow the deployment (set card)          DELETE /backup-sets/{s}/{s}/encryption  setEncryptionResetCommand
 *   Verify recoverable                        POST  /encryption/verify                verifyCommand
 *
 * The reads (policy, key list, copies) have verbs too (policyShowCommand,
 * keyListCommand, copiesCommand) and are shown where the page reads them.
 */
import { shellQuote } from "@shared/assurance/assuranceCommands";
import type { EncryptionMode, ExternalKeyReference, KeyProviderInput, SetEncryptionMode } from "@shared/types/encryption";

/** What stands in for a command reference on a line: never its arguments. */
const COMMAND_STAND_IN = "<the command that prints the key>";
const EXPORT_STAND_IN = "<a path to write the bundle to>";

function providerFlags(p: KeyProviderInput | undefined): string[] {
  if (!p) return [];
  if ("managed" in p) return ["--managed"];
  return referenceFlags(p.external);
}

function referenceFlags(r: ExternalKeyReference): string[] {
  if ("file" in r) return ["--external-file " + shellQuote(r.file)];
  if ("env" in r) return ["--external-env " + shellQuote(r.env)];
  return ["--external-command " + shellQuote(COMMAND_STAND_IN)];
}

export const policyShowCommand = () => "retnd encryption policy";

export function policyPatchCommand(patch: { mode?: EncryptionMode; keyProvider?: KeyProviderInput }): string {
  const parts = ["retnd encryption policy patch"];
  if (patch.mode) parts.push("--mode " + patch.mode);
  parts.push(...providerFlags(patch.keyProvider));
  return parts.join(" ");
}

export const policyResetCommand = () => "retnd encryption policy reset";

export function setEncryptionCommand(setId: string, mode: SetEncryptionMode, keyProvider?: KeyProviderInput): string {
  return ["retnd encryption backup-set " + shellQuote(setId), "--mode " + mode, ...providerFlags(keyProvider)].join(" ");
}

export const setEncryptionResetCommand = (setId: string) => "retnd encryption backup-set " + shellQuote(setId) + " --reset";

export const keyListCommand = () => "retnd encryption key list";
export const keyCreateCommand = () => "retnd encryption key create";
export const keyRotateCommand = () => "retnd encryption key rotate";
export const keyRetireCommand = (id: string) => "retnd encryption key retire " + shellQuote(id);
export const keyExportCommand = (id: string) => "retnd encryption key export " + shellQuote(id) + " --out " + shellQuote(EXPORT_STAND_IN);

export function keyConfirmCommand(id: string, digest?: string): string {
  return "retnd encryption key confirm " + shellQuote(id) + (digest ? " --digest " + shellQuote(digest) : "");
}

export function providerTestCommand(target: { managedKeyId: string } | { external: ExternalKeyReference }): string {
  return (
    "retnd encryption provider test " +
    ("managedKeyId" in target ? "--managed-key " + shellQuote(target.managedKeyId) : referenceFlags(target.external).join(" "))
  );
}

export const copiesCommand = (artifactId: string) => "retnd encryption copies " + shellQuote(artifactId);

export function verifyCommand(artifactId: string, opts: { medium?: string; allowEgress?: boolean } = {}): string {
  return (
    "retnd encryption verify " + shellQuote(artifactId) +
    (opts.medium ? " --medium " + shellQuote(opts.medium) : "") +
    (opts.allowEgress ? " --allow-egress" : "")
  );
}
