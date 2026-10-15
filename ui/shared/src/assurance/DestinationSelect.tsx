/**
 * The drop-down behind the configured destination choice (issue #1179), reached
 * through RecoveryTargetFields (issue #1190): every storage
 * destination Settings lists, or the words "None configured".
 *
 * It reads the same call Settings, Storage destinations does
 * (listStorageMediums), so the two cannot disagree about what exists. It used
 * to be a text box asking for a destination id, which made a person recall an
 * id from another screen and a typo a refusal after the fact.
 *
 * The value is the destination's id, which is what a recovery target takes
 * and what `retnd recover --to medium:<id>` takes.
 *
 * It reads through useAsync (the review of #1185), as the folder browser
 * does, so an ended session takes the app to sign-in instead of reading as
 * "the destinations could not be read".
 */
import { useApi } from "@shared/api/ApiContext";
import type { StorageMedium } from "@shared/api/contracts";
import { useAsync } from "@shared/hooks/useAsync";

function describe(m: StorageMedium): string {
  const where = m.isLocal ? "local backup root" : [m.type, m.bucket || m.path].filter(Boolean).join(", ");
  return m.id + " (" + where + ")";
}

export function DestinationSelect({ value, onChange, ariaLabel }: { value: string; onChange(id: string): void; ariaLabel: string }) {
  const api = useApi();
  const read = useAsync(() => api.listStorageMediums(), [api]);
  const failed = !read.loading && read.error !== null && read.data === null;
  const mediums = read.loading && read.data === null ? null : read.data;

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
