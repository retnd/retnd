/**
 * The words around a Recovery claim that has no result of its own (issue
 * #1134), shared by the chip and the Backup Sets card so both say them the
 * same way: the policy's effective state, and the set's newest test when it
 * was recorded on another backup.
 */
import "./assurance.css";
import { artifactPath } from "@shared/utilities/routes";
import { AssuranceLink } from "./AssuranceLink";
import { ageOf } from "./claims";
import { testWords } from "./verificationModel";
import type { RecoveryView } from "./verificationModel";

export function RecoveryNotes({ view }: { view: RecoveryView }) {
  const e = view.elsewhere;
  return (
    <>
      {view.untested ? <span className="aclaim__note" data-testid="recovery-untested">{view.untested}</span> : null}
      {view.effective ? <span className="aclaim__note" data-testid="recovery-effective">{view.effective}</span> : null}
      {e ? (
        <span className="aclaim__note" data-testid="recovery-elsewhere">
          {"Newest recovery test of this set: " + testWords(e.state) + " on "}
          <AssuranceLink to={artifactPath(e.artifactId)}>{e.filename}</AssuranceLink>
          {ageOf(e.at) ? ", " + ageOf(e.at) : ""}
        </span>
      ) : null}
    </>
  );
}

/** The label (a link when it has somewhere to go) and the notes, stacked. */
export function RecoveryLine({ view }: { view: RecoveryView }) {
  return (
    <span style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <span>{view.link ? <AssuranceLink to={view.link}>{view.label}</AssuranceLink> : view.label}</span>
      <RecoveryNotes view={view} />
    </span>
  );
}
