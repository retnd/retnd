/**
 * The `retnd` command each step of the configure-a-destination flow is
 * equivalent to (issue #669; EPIC G's standing rule, argued in full in
 * storageDestinationCommands.ts).
 *
 * # Nothing here can carry a secret, and there is nothing to redact
 *
 * The same structural property the sibling module has, for the same
 * reason: this CLI has no flag that takes credential material at all.
 * Material only ever arrives on stdin, through `medium
 * import-credentials --stdin`, which is not in the process table and not
 * in shell history. So every line below is the command that actually
 * works, byte for byte, with nothing starred out - and the reason it is
 * safe to print before the operator has typed a key is that there is no
 * key in it. The credential appears only as the reference that exchange
 * returned, which is what `medium configure --credentials-id` takes.
 *
 * # One verb for the whole form
 *
 * `medium configure <id> --backend B --field ID=VALUE` (#1154) takes a
 * destination in its backend's own field ids, so every field a manifest
 * declares has a spelling and there is no gap to print. That is what
 * replaced `medium add|edit` here (#1252): those take a fixed set of
 * flags and have no `--path`, so a local volume's line used to configure
 * less than the screen saved, with a note saying so.
 *
 * Each line is the one core/cliecho prints for the request the wizard
 * sends (its `medium configure` arms, routes.go), so the line beside a
 * control and the line the terminal records after it are one command.
 * The one deliberate difference is `--backend`: the wizard leaves the
 * backend out of the request when the destination is already declared,
 * and the echo follows the body, but `medium configure` checks that it
 * matches when it is given, so naming it on every line costs nothing and
 * makes the line say what kind of destination it is.
 */
import type { BackendManifest } from "@shared/api/contracts";
import { shellQuote } from "@shared/assurance/assuranceCommands";

/**
 * `medium configure <id> --backend B --field id=value ...`, for the values
 * the wizard is about to send.
 *
 * Fields go on the line sorted by id, because that is the order the
 * request body carries them in (toWireMediumConfiguration sorts), and so
 * the order the server's echo prints. A credential-kind field is never a
 * value on a command line; the credential is the reference, or nothing,
 * and nothing means `medium configure` keeps the one already configured.
 */
export function configureCommand(
  manifest: BackendManifest,
  destinationId: string,
  values: Record<string, string>,
  credentialsId: string | null = null
): string {
  const credentialKind: Record<string, true> = {};
  for (const field of manifest.fields) if (field.kind === "credential") credentialKind[field.id] = true;

  const parts = [`retnd medium configure ${shellQuote(destinationId)}`, `--backend ${shellQuote(manifest.id)}`];
  for (const id of Object.keys(values).sort()) {
    if (credentialKind[id]) continue;
    const raw = values[id];
    // `medium configure` refuses an empty value, and the request leaves
    // one out rather than sending it.
    if (!raw) continue;
    const value = id === "endpoint" ? endpointWithoutUserinfo(raw) : raw;
    parts.push(`--field ${shellQuote(`${id}=${value}`)}`);
  }
  if (credentialsId) parts.push(`--credentials-id ${shellQuote(credentialsId)}`);
  return parts.join(" ");
}

/** The test step's line: the same configuration, proven and not written.
 *  The step checks the values on screen, which are not saved yet, so its
 *  twin is the write-nothing form of the save and not `medium
 *  test-connection`, which checks what is already saved. */
export function preflightCommand(
  manifest: BackendManifest,
  destinationId: string,
  values: Record<string, string>,
  credentialsId: string | null = null
): string {
  return `${configureCommand(manifest, destinationId, values, credentialsId)} --preflight`;
}

/** `medium default <id>`, the review step's second act, printed only
 *  while the box asking for it is ticked. A separate line because it is
 *  a separate request, made after the save (#622). The destinations
 *  list's Make default button prints the same line from the same
 *  function. */
export { setDefaultCommand as defaultCommand } from "@shared/pages/storageDestinationCommands";

/** `medium import-credentials --stdin`: the one command that ever
 *  handles material, and it takes no argument at all - which is the
 *  point, and why this line is printable before anything is typed. */
export function importCredentialsCommand(): string {
  return "retnd medium import-credentials --stdin";
}

/** A credential can be spelled into a URL's userinfo, and these lines are
 *  copy-to-clipboard and exportable, so it comes out. The same cut
 *  core/cliecho makes (endpointWithoutUserinfo, cliecho.go). */
function endpointWithoutUserinfo(raw: string): string {
  let scheme = "";
  let rest = raw;
  const sep = rest.indexOf("://");
  if (sep >= 0) {
    scheme = rest.slice(0, sep + 3);
    rest = rest.slice(sep + 3);
  }
  let authority = rest;
  let tail = "";
  const end = rest.search(/[/?#]/);
  if (end >= 0) {
    authority = rest.slice(0, end);
    tail = rest.slice(end);
  }
  const at = authority.lastIndexOf("@");
  if (at >= 0) authority = authority.slice(at + 1);
  return scheme + authority + tail;
}
