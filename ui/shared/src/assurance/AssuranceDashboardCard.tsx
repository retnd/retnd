/**
 * Due, overdue, degraded and no recovery test, on the Dashboard (issues #985
 * and #1134).
 *
 * The counts come from the verification model's tally, which counts through
 * `claims.bucketsOf` and so counts exactly what the Backups list shows when
 * a count is followed. Each count is a link to that list, named in the style
 * of the activity dock's level toggles ("Show 2 backups with no recovery
 * test"); a zero is a disabled button with its reason beside it, because a
 * link to an empty list is a link that goes nowhere.
 *
 * A zero is a zero, never a green tile: "nothing is due" and "nothing has
 * ever been tested" are different statements. Unknown is due, and a read that
 * failed says unknown rather than zero. The fourth count, no recovery test, is
 * what keeps a deployment nobody has tested from reading as all clear: it says
 * so in a sentence under the tiles whenever it is not zero.
 */
import "./assurance.css";
import { useApi } from "@shared/api/ApiContext";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { AssuranceLink } from "./AssuranceLink";
import { BUCKET_NONE, bucketLinkName } from "./buckets";
import type { Bucket } from "./claims";
import { DEFAULTS_LINK, useFactsIndex, useTally } from "./verificationModel";

const LABEL: Record<Bucket, string> = { due: "Due", overdue: "Overdue", degraded: "Degraded", untested: "No recovery test" };
const ORDER: Bucket[] = ["due", "overdue", "degraded", "untested"];

export function AssuranceDashboardCard() {
  const api = useApi();
  const tally = useTally();
  const facts = useFactsIndex();
  if (!api.assurance) return null;
  const known = tally.known;
  // How many of the untested backups sit in a set on the default configuration:
  // the reason there is nothing to test with, said beside the count.
  const onDefault = tally.ids.untested.filter((id) => facts.byArtifact[id]?.recoveryView.basis === "default").length;
  const cell = (b: Bucket) => {
    const n = tally[b];
    const why = "assurance-" + b + "-why";
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: 2, maxWidth: 190 }} data-testid={"assurance-" + b} key={b}>
        <span className="aclaim__lab">{LABEL[b]}</span>
        {!known ? (
          <b style={{ fontSize: 22 }}>unknown</b>
        ) : n > 0 ? (
          <AssuranceLink to={"/backups?assurance=" + b} label={bucketLinkName(b, n)}><b style={{ fontSize: 22 }}>{n}</b></AssuranceLink>
        ) : (
          <>
            <button className="btn btn--quiet" disabled aria-label={bucketLinkName(b, 0)} aria-describedby={why} title={BUCKET_NONE[b]} style={{ alignSelf: "flex-start" }}>
              <b style={{ fontSize: 22 }}>0</b>
            </button>
            <span id={why} data-testid={why} style={{ color: "var(--text-2)", fontSize: "var(--text-xs)" }}>{BUCKET_NONE[b]}</span>
          </>
        )}
      </div>
    );
  };
  return (
    <section className="card" aria-label="Assurance summary">
      <div className="card__header">
        <InfoTooltip id="assurance.dashboard.card"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", gap: 32, flexWrap: "wrap" }}>
        {ORDER.map(cell)}
        {facts.error ? <span role="alert">Could not read assurance, so these are unknown rather than zero.</span> : null}
        {known && tally.untested > 0 ? (
          <p data-testid="assurance-untested-note" style={{ flexBasis: "100%", margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
            {"No recovery test is on record for " + tally.untested + " of " + tally.total + (tally.total === 1 ? " backup" : " backups") +
              ", so a zero in Due does not mean they were tested."}
            {onDefault > 0 ? (
              <>
                {" " + onDefault + " of them are in sets on the "}
                <AssuranceLink to={DEFAULTS_LINK}>default configuration</AssuranceLink>
                {"."}
              </>
            ) : null}
          </p>
        ) : null}
      </div>
    </section>
  );
}
