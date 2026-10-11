/**
 * The three claims of one backup, as three separate things (issue #985).
 *
 * There is deliberately no combined badge and nothing in this file that
 * could produce one. Each chip comes from `renderClaim`/`renderStored`, which
 * are pure functions of one record, so a chip can only be a pass when its
 * own record says so with its own class and time.
 *
 * `available` is false when the server does not offer the assurance routes
 * (api.assurance is undefined). Every chip is then `unknown` and the group
 * says why, which is what keeps an old server from looking assured.
 *
 * # A chip being re-checked
 *
 * It keeps the ink it had. An earlier version dimmed the whole chip with
 * `opacity: 0.6`, which composites the text with what is behind it and took
 * the age text to 2.50:1 (light) and 3.05:1 (dark) and the label to 4.25:1
 * (light), under the 4.5:1 minimum. The cue is now a dashed outline, italic
 * type, the words "re-checking" and `aria-busy`, none of which touches a
 * colour (assurance.css, and the computed check in test/assurance-contrast).
 *
 * # A copy that failed
 *
 * The chips are claims about the backup. A copy whose last scrub or recovery
 * test failed is said once more, about that copy, under them: which copy,
 * what ran, how many times in a row and what the journal recorded.
 */
import "./assurance.css";
import { Icon } from "@shared/design-system/icons";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { failureSentence, renderClaim, renderStored } from "./claims";
import type { Glyph } from "./claims";
import type { ArtifactAssurance } from "./types";

export function ClaimChips({
  assurance, available = true, checking = false, recoveryReported = true
}: {
  assurance: ArtifactAssurance | null | undefined;
  available?: boolean;
  /** A scrub or recovery test is running against this backup, so what the
   *  chips show is the previous answer and is drawn as such. */
  checking?: boolean;
  /** False when the server has no route that reports recovery tests, so an
   *  Unknown Recovery claim says why instead of looking like a failure to ask. */
  recoveryReported?: boolean;
}) {
  const known = available ? assurance ?? undefined : undefined;
  const stored = renderStored(known?.copies);
  const content = renderClaim("content", known?.content);
  const recovery = renderClaim("recovery", known?.recovery);
  const chip = (
    which: string, lab: string, state: string, glyph: Glyph, label: string, age?: string, check = false
  ) => (
    <div className="aclaim" data-claim={which} data-state={state} data-checking={check ? "true" : undefined} aria-busy={check ? true : undefined}>
      <span className="aclaim__lab">{lab}</span>
      <span className="achip" data-chip="" role="group" aria-label={lab + ": " + label + (age ? " " + age : "") + (check ? " (re-checking)" : "")}>
        <span className="achip__glyph" data-glyph={glyph.id} aria-hidden="true">{glyph.icon ? <Icon name={glyph.icon} /> : glyph.text}</span>
        {label}
        {age ? <span className="achip__age">{age}</span> : null}
        {check ? <span className="achip__checking">re-checking</span> : null}
      </span>
    </div>
  );
  const failures = known?.copyFailures ?? [];
  return (
    <div className="aclaims" data-testid="assurance-claims" title={available ? undefined : "This server does not report assurance yet"}>
      <InfoTooltip id="assurance.claim.stored">
        {chip("stored", "Stored", stored.state, { id: "stored-" + stored.state, text: stored.state === "stored" ? "\u25A0" : stored.state === "no_copy" ? "\u2212" : "?" }, stored.label)}
      </InfoTooltip>
      <InfoTooltip id="assurance.claim.contentVerified">
        {chip("content", "Content", content.state, content.glyph, content.label, content.age, checking)}
      </InfoTooltip>
      <InfoTooltip id="assurance.claim.recoveryTested">
        {chip("recovery", "Recovery", recovery.state, recovery.glyph, recovery.label, recovery.age, checking)}
      </InfoTooltip>
      {failures.length > 0 ? (
        <ul className="afails" data-testid="copy-failures" aria-label="Copies whose last check did not pass">
          {failures.map((f) => (
            <li key={f.kind + ":" + f.medium} className="afail" data-failure-kind={f.kind} data-failure-state={f.state} data-medium={f.medium}>
              {failureSentence(f)}
            </li>
          ))}
        </ul>
      ) : null}
      {available && !recoveryReported && recovery.state === "unknown" ? (
        <div data-testid="recovery-unknown-why" style={{ flexBasis: "100%", color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
          This server has no route that reports recovery tests, so the Recovery claim is Unknown rather than a pass.
        </div>
      ) : null}
    </div>
  );
}
