/**
 * The set page's Validation block (issues #985 and #1134): what has actually
 * run for this set, then what the set is configured to run.
 *
 * It used to be the second half only, drawn from `BackupSet.validations`,
 * which the client fills with an empty list for every set because nothing on
 * the wire carries it. All three rows therefore read "not enabled" for a set
 * whose content had been verified and whose structure had been read, and an
 * empty list is "this server did not say", not "off". The first half is the
 * verification model's, so this block, the chips above it and the lifecycle
 * on the backup's own page are one set of facts.
 */
import { Icon } from "@shared/design-system/icons";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import { stamp } from "@shared/utilities/format";
import type { BackupSet } from "@shared/types/backup";
import { useSetFacts } from "./verificationModel";

const TIPS: Record<"transfer" | "checksum" | "application", TooltipId> = {
  transfer: "sets.detail.validation.transfer",
  checksum: "sets.detail.validation.checksum",
  application: "sets.detail.validation.application"
};

const LABELS = { transfer: "Transfer verification", checksum: "Checksum verification (SHA-256)", application: "Application validation" } as const;

const fileOf = (id: string) => id.split("/").pop() ?? id;

export function ValidationBlock({ set }: { set: BackupSet }) {
  const facts = useSetFacts(set.id);
  const test = facts?.setTest ?? null;
  const content = facts?.newestContent ?? null;
  // An empty list is the client's "nothing reported", so a row cannot honestly
  // say "not enabled"; a fixture that names some stages does mean the rest are off.
  const reported = set.validations.length > 0;
  return (
    <div data-testid="validation-block" style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      {facts ? (
        <ul style={{ margin: 0, padding: 0, listStyle: "none", display: "flex", flexDirection: "column", gap: 8, fontSize: 13 }} aria-label="Recorded results">
          {test ? (
            test.state === "readable" ? (
              <li data-testid="validation-ran-structure">
                {"Built-in structure check: readable " + stamp(test.at) + ", on " + fileOf(test.artifactId) + ". It proves the file decodes end to end and matches its digest, not that an application can use it."}
              </li>
            ) : (
              <li data-testid="validation-ran-test">
                {"Recovery test: " + test.state.replace(/_/g, " ") + " " + stamp(test.at) + ", on " + fileOf(test.artifactId) + "."}
              </li>
            )
          ) : null}
          {content ? (
            <li data-testid="validation-ran-content">
              {"Content verified " + stamp(content.at) + ", on " + fileOf(content.artifactId) + ". A scrub read the copy back and its hash matched."}
            </li>
          ) : null}
          {!test && !content ? (
            <li data-testid="validation-ran-none">Nothing has been recorded for this set yet: no scrub has verified a backup and no recovery test has run.</li>
          ) : null}
        </ul>
      ) : null}
      <ul style={{ margin: 0, padding: 0, listStyle: "none", display: "flex", flexDirection: "column", gap: 10, fontSize: 13 }}>
        {(["transfer", "checksum", "application"] as const).map((v) => {
          const on = set.validations.includes(v);
          return (
            <li key={v} style={{ display: "flex", gap: 9 }}>
              {/* The tick is artwork after #621 and the en dash beside it is
                  still an en dash: one is a verdict and the other is "this is
                  off", which is a thing a dash says and a picture does not. */}
              <span aria-hidden="true" style={{ color: on ? "var(--ok)" : "var(--text-2)" }}>
                {on ? <Icon name="success" /> : "–"}
              </span>
              <InfoTooltip id={TIPS[v]}>
                <span>
                  {LABELS[v]}
                  {on ? null : <span style={{ color: "var(--text-2)" }}>{reported ? " — not enabled" : " — not reported by this server"}</span>}
                </span>
              </InfoTooltip>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
