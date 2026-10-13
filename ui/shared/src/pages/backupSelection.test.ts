import { describe, expect, it } from "vitest";
import { inReceivedRange, receivedDay, unselectableReason } from "./backupSelection";
import type { BackupArtifact } from "@shared/types/backup";

const base = { receivedAt: "2026-08-28T23:30:00-04:00", placements: [] } as unknown as BackupArtifact;
const copy = (access: string) => ({ ...base, placements: [{ access, status: "ACTIVE" }] }) as unknown as BackupArtifact;

describe("what the Backups list lets you select", () => {
  it("reads the received day in the offset the record carries, not the viewer's", () => {
    expect(receivedDay(base)).toBe("2026-08-28");
  });
  it("keeps both bounds inclusive and an empty bound open", () => {
    expect(inReceivedRange(base, "2026-08-28", "2026-08-28")).toBe(true);
    expect(inReceivedRange(base, "2026-08-29", "")).toBe(false);
    expect(inReceivedRange(base, "", "2026-08-27")).toBe(false);
    expect(inReceivedRange(base, "", "")).toBe(true);
  });
  it("refuses a backup with no copy or no reachable copy, with a reason, and allows the rest", () => {
    expect(unselectableReason(base)).toMatch(/No copy/);
    expect(unselectableReason(copy("unreachable"))).toMatch(/cannot reach/);
    expect(unselectableReason(copy("immediate"))).toBeNull();
    expect(unselectableReason(copy("requires_restore"))).toBeNull();
  });
});
