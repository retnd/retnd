/**
 * Issue #1303: a standalone decrypt-verify rides the scrub operation, whose
 * record now carries bytes_read. The decrypt row draws that figure, the CLI
 * prints the same one. Written by reading and not run.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { httpApi } from "@shared/api/client";
import type { AssuranceOperationRecord } from "@shared/api/contracts";
import { verifyRows } from "@shared/encryption/verifyRows";

const rec = (over: Partial<AssuranceOperationRecord>): AssuranceOperationRecord => ({
  operationId: "op-1", kind: "scrub", backupSetId: "src/set", state: "running", stage: "", reason: "",
  startedAt: "2026-11-10T09:00:00Z", endedAt: "", events: [], ...over
});

describe("#1303 a standalone decrypt-verify's byte figure", () => {
  afterEach(() => { vi.unstubAllGlobals(); });
  const serve = (b: unknown) => vi.stubGlobal("fetch", vi.fn(async () => ({
    ok: true, status: 200, headers: { get: () => null }, json: async () => b, text: async () => JSON.stringify(b)
  })));
  const wire = (extra: Record<string, unknown>) => ({
    operation_id: "op-1", kind: "scrub", backup_set_id: "src/set", state: "running", stage: "", reason: "",
    started_at: "", ended_at: "", events: [], ...extra
  });

  it("draws the record's bytesRead on the decrypt row", () => {
    expect(verifyRows(rec({ bytesRead: 196608 })).find((r) => r.id === "decrypt")?.label).toBe("Decrypt-verifying 192.0 KB");
  });

  it("draws no figure when the record carries none, and a failed run stays failed", () => {
    expect(verifyRows(rec({})).find((r) => r.id === "decrypt")?.label).toBe("Decrypt-verifying");
    expect(verifyRows(rec({ state: "failed", bytesRead: 65536 })).find((r) => r.id === "decrypt")?.state).toBe("failed");
  });

  it("maps bytes_read off the wire, and leaves it undefined when the server sent none", async () => {
    serve(wire({ bytes_read: 4096 }));
    await expect(httpApi.getAssuranceOperation("op-1")).resolves.toMatchObject({ bytesRead: 4096 });
    serve(wire({}));
    expect((await httpApi.getAssuranceOperation("op-1")).bytesRead).toBeUndefined();
  });
});
