/**
 * Due, overdue and degraded, on the Dashboard (issue #985).
 *
 * Three counters in the claim vocabulary, computed from the same records the
 * Backups page shows. A zero is a zero, never a green tile: "nothing is due"
 * and "nothing has ever run" are different statements, so the card says when
 * the answer is unknown rather than counting it as none. Unknown is due.
 */
import "./assurance.css";
import { useApi } from "@shared/api/ApiContext";
import { useAsync } from "@shared/hooks/useAsync";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { tally } from "./claims";

export function AssuranceDashboardCard() {
  const api = useApi();
  const list = useAsync(() => (api.assurance ? api.assurance.listArtifactAssurance() : Promise.resolve(null)), [api]);
  if (!api.assurance) return null;
  const counts = list.data ? tally(list.data) : null;
  const cell = (label: string, value: number | null) => (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }} data-testid={"assurance-" + label.toLowerCase()}>
      <span className="aclaim__lab">{label}</span>
      <b style={{ fontSize: 22 }}>{value === null ? "unknown" : value}</b>
    </div>
  );
  return (
    <section className="card" aria-label="Assurance summary">
      <div className="card__header">
        <InfoTooltip id="assurance.dashboard.card"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", gap: 32, flexWrap: "wrap" }}>
        {cell("Due", counts?.due ?? null)}
        {cell("Overdue", counts?.overdue ?? null)}
        {cell("Degraded", counts?.degraded ?? null)}
        {list.error ? <span role="alert">Could not read assurance, so these are unknown rather than zero.</span> : null}
      </div>
    </section>
  );
}
