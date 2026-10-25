/**
 * The `retnd` command each step of the configure-a-destination flow is
 * equivalent to (issue #669; EPIC G's standing rule, argued in full in
 * storageDestinationCommands.ts).
 *
 * # Nothing here can carry a secret
 *
 * The same structural property the sibling module has, for the same
 * reason: this CLI has no flag that takes credential material at all.
 * Material only ever arrives on stdin, through `medium
 * import-credentials --stdin`, which is not in the process table and not
 * in shell history. The credential appears only as the reference that
 * exchange returned, which is what `medium configure --credentials-id`
 * takes, and a credential-kind field never goes on a line.
 *
 * The one place a secret could still ride along is a URL's userinfo, so a
 * url-kind field is cut the way core/cliecho cuts it (see
 * endpointWithoutUserinfo below). That is the line's one deliberate
 * difference from the request it stands for: the request keeps whatever
 * was typed, the line does not, and the wizard says so under the line.
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
 * Each line describes the same configuration as the request the wizard
 * sends, and it is the line core/cliecho echoes for that request (its
 * `medium configure` arms in routes.go). The wizard names the backend on
 * every request, so `--backend` is on both. That agreement is checked,
 * not assumed: core/cliecho/testdata/medium_configure_1252.json holds
 * cases the Go suite and this module's tests both run.
 */
import type { BackendManifest } from "@shared/api/contracts";
import { shellQuote } from "@shared/assurance/assuranceCommands";

/** What stands in for a value the userinfo cut could not read. The same
 *  words core/cliecho uses (withheldValue), printed bare between "<" and
 *  ">" the way its placeholders are, so the line visibly won't run. */
export const WITHHELD = "the value, left out because it looks like it carries credentials";

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
  const credentialIds = idsOfKind(manifest, "credential");
  const urlIds = idsOfKind(manifest, "url");

  const parts = [`retnd medium configure ${shellQuote(destinationId)}`, `--backend ${shellQuote(manifest.id)}`];
  for (const id of Object.keys(values).sort()) {
    if (credentialIds.has(id)) continue;
    const raw = values[id];
    // `medium configure` refuses an empty value, and the request leaves
    // one out rather than sending it.
    if (!raw) continue;
    if (urlIds.has(id)) {
      const cut = endpointWithoutUserinfo(raw);
      if (cut.withheld) {
        parts.push(`--field <${id}=${WITHHELD}>`);
        continue;
      }
      parts.push(`--field ${shellQuote(`${id}=${cut.value}`)}`);
      continue;
    }
    parts.push(`--field ${shellQuote(`${id}=${raw}`)}`);
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

/**
 * Whether the line differs from the request because of a url field, and
 * how: "stripped" when userinfo was cut out (running the line configures
 * the address without it), "withheld" when the cut could not read the
 * address and a placeholder stands in (the line won't run as printed), or
 * null when the line carries every url field as typed. "withheld" wins
 * when there are both, because it is the one that stops the line running.
 */
export function endpointNote(
  manifest: BackendManifest,
  values: Record<string, string>
): "stripped" | "withheld" | null {
  let note: "stripped" | null = null;
  for (const id of idsOfKind(manifest, "url")) {
    const raw = values[id];
    if (!raw) continue;
    const cut = endpointWithoutUserinfo(raw);
    if (cut.withheld) return "withheld";
    if (cut.value !== raw) note = "stripped";
  }
  return note;
}

/** `medium import-credentials --stdin`: the one command that ever
 *  handles material, and it takes no argument at all - which is the
 *  point, and why this line is printable before anything is typed. */
export function importCredentialsCommand(): string {
  return "retnd medium import-credentials --stdin";
}

/**
 * A credential can be spelled into a URL's userinfo, and these lines are
 * copy-to-clipboard and exportable, so it comes out. The same cut
 * core/cliecho makes (endpointWithoutUserinfo in cliecho.go), checked
 * against the same vectors.
 *
 * The authority ends at the first "/", "?" or "#", so a password with one
 * of those in it (https://u:pa/ss@host) ends it early and the cut finds
 * no "@" to cut at. The result is checked rather than trusted: any "@"
 * left in it means the address did not split the way the cut assumes, and
 * the value is withheld. That also withholds an address with a real "@"
 * in its path, which is the safe direction to be wrong in.
 */
export function endpointWithoutUserinfo(raw: string): { value: string; withheld: boolean } {
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
  const value = scheme + authority + tail;
  return value.includes("@") ? { value: "", withheld: true } : { value, withheld: false };
}

function idsOfKind(manifest: BackendManifest, kind: BackendManifest["fields"][number]["kind"]): Set<string> {
  return new Set(manifest.fields.filter((f) => f.kind === kind).map((f) => f.id));
}
