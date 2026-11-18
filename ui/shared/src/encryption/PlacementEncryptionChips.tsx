/**
 * One Backups list row's Encryption cell (EPIC N, issue #995; design section 5):
 * one chip pair per copy, in destination order, from each placement's own
 * fields. Nothing is drawn when no copy is encrypted, so a deployment with
 * encryption off sees the list it always had (the column itself is gated the
 * same way by the page).
 */
import type { BackupPlacement } from "@shared/types/backup";
import { LOCAL_DESTINATION_ID } from "@shared/api/contracts";
import { CopyChips } from "./EncryptionChips";
import { anyPlacementEncrypted, copyEncryptionView, placementEncryption } from "./encryptionState";

export function PlacementEncryptionChips({ placements }: { placements: BackupPlacement[] }) {
  if (!anyPlacementEncrypted(placements)) return null;
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 10 }}>
      {placements.map((p) => (
        <div key={p.medium + "|" + p.location} data-list-copy={p.medium} style={{ display: "flex", flexDirection: "column", gap: 2 }}>
          <span style={{ fontSize: "var(--text-xs)", color: "var(--text-2)" }}>{p.medium === LOCAL_DESTINATION_ID ? "Local" : p.medium}</span>
          <CopyChips view={copyEncryptionView(placementEncryption(p), p)} />
        </div>
      ))}
    </div>
  );
}
