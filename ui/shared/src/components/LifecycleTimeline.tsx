/**
 * What happened to one backup, in order, and what has not happened yet.
 *
 * The phases are derived from recorded timestamps rather than authored,
 * which is what keeps this honest about the one step that matters. Remote
 * deletion can only ever appear as done when the service recorded doing
 * it, and the service records that only after the local copy is verified
 * and durably committed, so there is no arrangement of this component that
 * can show the original as removed before the copy was safe.
 *
 * An unreached phase renders with a dash and no time. Not a zero, not a
 * projection: the timeline says where this artifact actually got to.
 *
 * The two verification steps that follow the commit (content verified, and
 * structure readable or recovery test passed) are read from the verification
 * model (issue #1134), the same facts the chips, the Copies block and the
 * Validation block read, so the timeline cannot say a backup was never
 * verified while the page beside it says it was. They are drawn only when the
 * model was asked about this backup.
 *
 * `buildPhases` is exported separately from the rendering so the ordering
 * rule can be asserted without a DOM, which is what LifecycleTimeline.test
 * does.
 */
import type { BackupArtifact } from "@shared/types/backup";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import { clock } from "@shared/utilities/format";
import type { BackupFacts } from "@shared/assurance/verificationModel";

interface Phase {
  label: string;
  detail: string;
  at: string | null;
  terminalRemote?: boolean;
  /** The registry entry explaining what this phase means and what its
   *  unreached state says (issue #834). Authored per phase rather than
   *  derived from the label, so the compiler checks every one of them. */
  tip: TooltipId;
}

/** The timeline is derived, not authored: remote deletion can only ever render
 *  AFTER commit because it is read from remoteSourceRemovedAt, which the service
 *  sets only once the safe state is persisted (§14, §15). */
export function buildPhases(artifact: BackupArtifact, facts?: BackupFacts | null): Phase[] {
  const phases: Phase[] = [
    { label: "DISCOVERED", detail: "Completion signal seen on the remote server", at: artifact.producedAt, tip: "backups.lifecycle.discovered" },
    { label: "TRANSFERRED", detail: "Received over SFTP", at: artifact.receivedAt, tip: "backups.lifecycle.transferred" },
    {
      label: "VERIFIED",
      detail: artifact.checksumAlgorithm.toUpperCase() + " matched the producer manifest",
      at: artifact.validation === "verified" ? artifact.receivedAt : null,
      tip: "backups.lifecycle.verified"
    },
    {
      label: "COMMITTED",
      detail: "Durably written and fsynced to NAS storage",
      at: artifact.validation === "verified" ? artifact.receivedAt : null,
      tip: "backups.lifecycle.committed"
    },
    {
      label: "SAFE STATE PERSISTED",
      detail: "Catalog records this artifact as known-good",
      at: artifact.validation === "verified" ? artifact.receivedAt : null,
      tip: "backups.lifecycle.safe-state"
    },
    ...(facts ? verificationPhases(facts) : []),
    {
      label: "REMOTE SOURCE DELETED",
      detail: artifact.remoteSourceRemovedAt
        ? "Original removed from the remote server after commit"
        : "Pending \u2014 the remote original is still retained",
      at: artifact.remoteSourceRemovedAt,
      terminalRemote: true,
      tip: "backups.lifecycle.remote-deleted"
    }
  ];
  return phases;
}

/** What the scrubs and recovery tests have recorded for this backup, as steps.
 *  An unreached step says so and says what would reach it. */
function verificationPhases(facts: BackupFacts): Phase[] {
  const check = facts.structure?.check;
  return [
    {
      label: "CONTENT VERIFIED",
      detail: facts.contentAt ? "A scrub read the copy back and its hash matched" : "No scrub has read this backup back and matched its hash",
      at: facts.contentAt ?? null,
      tip: "backups.lifecycle.content-verified"
    },
    {
      label: check === "validator" ? "RECOVERY TEST PASSED" : "STRUCTURE READABLE",
      detail: check === "builtin"
        ? "The built-in check recovered the copy and read it end to end"
        : check === "validator"
          ? "The operator's validator accepted the recovered copy"
          : "No recovery test has run on this backup",
      at: facts.structure?.at ?? null,
      tip: "backups.lifecycle.structure-readable"
    }
  ];
}

export function LifecycleTimeline({ artifact, facts }: { artifact: BackupArtifact; facts?: BackupFacts | null }) {
  const phases = buildPhases(artifact, facts);

  return (
    <ol style={{ margin: 0, padding: "20px 22px 22px", listStyle: "none" }}>
      {phases.map((p, i) => {
        const reached = p.at !== null;
        const last = i === phases.length - 1;
        return (
          <li
            key={p.label}
            style={{
              display: "grid", gridTemplateColumns: "18px 1fr auto",
              gap: 14, alignItems: "start"
            }}
          >
            <span
              aria-hidden="true"
              style={{
                display: "grid", placeItems: "center", paddingTop: 3, position: "relative",
                background: last
                  ? undefined
                  : "linear-gradient(var(--border), var(--border)) no-repeat center / 1px 100%"
              }}
            >
              <span
                style={{
                  display: "block", width: 9, height: 9, borderRadius: "50%",
                  outline: "3px solid var(--surface)",
                  background: !reached
                    ? "var(--border-strong)"
                    : p.terminalRemote
                      ? "var(--warn)"
                      : "var(--ok)"
                }}
              />
            </span>
            {/* One host per phase, wrapping the phase's own text rather
                than the whole row: the marker and the time beside it are
                separate hover regions, and a host around the <li> would
                put a <span> between the list and its item. */}
            <InfoTooltip id={p.tip} block>
              <div style={{ paddingBottom: 18 }}>
                <div
                  style={{
                    fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)",
                    letterSpacing: "0.07em", fontWeight: 600,
                    color: reached ? "var(--text)" : "var(--text-2)"
                  }}
                >
                  {p.label}
                </div>
                <div style={{ marginTop: 3, fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
                  {p.detail}
                </div>
              </div>
            </InfoTooltip>
            <InfoTooltip id="backups.lifecycle.time" block alignEnd>
              <div
                style={{
                  fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)",
                  color: "var(--text-2)", paddingTop: 1
                }}
              >
                {p.at ? clock(p.at) : "\u2014"}
              </div>
            </InfoTooltip>
          </li>
        );
      })}
    </ol>
  );
}
