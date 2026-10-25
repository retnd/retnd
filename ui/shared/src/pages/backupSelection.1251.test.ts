/**
 * The review of #1256: the received-date range was read off receivedAt, which
 * the client maps from updated_at, and updated_at moves on every transition. A
 * backup that arrived in March and whose set went read-only and back in
 * October read as received in October. The range reads producedAt (the wire's
 * discovered_at), which is set once when the backup arrives, the same field
 * `retnd artifacts --since/--until` and `recover --set` read.
 */
import { describe, expect, it } from "vitest";
import { inReceivedRange, receivedDay } from "./backupSelection";
import type { BackupArtifact } from "@shared/types/backup";

const flipped = { producedAt: "2026-03-15T23:30:00Z", receivedAt: "2026-10-20T12:00:00Z", placements: [] } as unknown as BackupArtifact;

describe("#1251 the range is about when a backup arrived, not when it last changed", () => {
  it("#1251 the day is the arrival day", () => {
    expect(receivedDay(flipped)).toBe("2026-03-15");
  });
  it("#1251 a later transition does not move a backup into or out of the range", () => {
    expect(inReceivedRange(flipped, "2026-03-15", "2026-03-15")).toBe(true);
    expect(inReceivedRange(flipped, "2026-10-20", "")).toBe(false);
    expect(inReceivedRange(flipped, "", "2026-03-14")).toBe(false);
  });
});
