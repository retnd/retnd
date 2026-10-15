/**
 * The drop-down behind "Configured destination" (issue #1179): every storage
 * destination Settings lists, or the words "None configured".
 *
 * It reads the same call Settings, Storage destinations does
 * (listStorageMediums), so the two cannot disagree about what exists. It used
 * to be a text box asking for a destination id, which made a person recall an
 * id from another screen and a typo a refusal after the fact.
 *
 * The value is the destination's id, which is what a recovery target takes
 * and what `retnd recover --to medium:<id>` takes.
 */
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { StorageMedium } from "@shared/api/contracts";

function describe(m: StorageMedium): string {
  const where = m.isLocal ? "local backup root" : [m.type, m.bucket || m.path].filter(Boolean).join(", ");
  return m.id + " (" + where + ")";
}

export function DestinationSelect({ value, onChange, ariaLabel }: { value: string; onChange(id: string): void; ariaLabel: string }) {
  const api = useApi();
  const [mediums, setMediums] = useState<StorageMedium[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    api.listStorageMediums().then((x) => { if (live) setMediums(x); }, () => { if (live) setFailed(true); });
    return () => { live = false; };
  }, [api]);

  const none = mediums !== null && mediums.length === 0;
  return (
    <label className="field">
      <span className="field__label">Destination (the ones in Settings, Storage destinations)</span>
      <select
        className="select"
        aria-label={ariaLabel}
        value={value}
        disabled={none || failed || mediums === null}
        onChange={(e) => onChange(e.target.value)}
      >
        {mediums === null && !failed ? <option value="">Reading the destinations...</option> : null}
        {failed ? <option value="">The destinations could not be read</option> : null}
        {none ? <option value="">None configured</option> : null}
        {mediums && mediums.length > 0 ? (
          <>
            <option value="">Choose a destination</option>
            {mediums.map((m) => <option key={m.id} value={m.id}>{describe(m)}</option>)}
          </>
        ) : null}
      </select>
    </label>
  );
}
