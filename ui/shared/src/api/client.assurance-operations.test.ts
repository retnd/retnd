/**
 * The wire shapes of the three operator controls (issue #985): Scrub now,
 * Cancel and Show validator output, plus the `coalesced` flag a recovery
 * test's start now carries. Each assertion is about a detail `tsc` cannot
 * hold: which path and verb a method calls, which header rides a POST, and
 * what an empty record becomes on the way into a domain type.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { httpApi } from "./client";

function fetchReturning(body: unknown, status = 200) {
  return vi.fn().mockResolvedValue({ ok: status < 400, status, headers: new Headers(), json: async () => body });
}
const lastCall = (f: ReturnType<typeof vi.fn>) => {
  const [url, init] = f.mock.calls[f.mock.calls.length - 1] as [string, RequestInit | undefined];
  return { url, method: init?.method ?? "GET", headers: new Headers(init?.headers) };
};

describe("the operator controls on the wire", () => {
  beforeEach(() => { document.cookie = "retnd_csrf=tok"; window.history.pushState({}, "", "/"); });
  afterEach(() => { vi.unstubAllGlobals(); document.cookie = "retnd_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT"; });

  it("startScrub is POST /backup-sets/{source}/{set}/scrub with the CSRF token, and maps the 202", async () => {
    const f = fetchReturning({ backup_set_id: "src/set-1", operation_id: "op-s" }, 202);
    vi.stubGlobal("fetch", f);
    const got = await httpApi.startScrub("src", "set-1");
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/backup-sets/src/set-1/scrub");
    expect(c.method).toBe("POST");
    expect(c.headers.get("X-CSRF-Token")).toBe("tok");
    expect(got).toEqual({ backupSetId: "src/set-1", operationId: "op-s" });
  });

  it("cancelAssuranceOperation is POST /assurance/operations/{op}/cancel, outside /operations, and maps the 202", async () => {
    const f = fetchReturning({ operation_id: "op/s 1", kind: "scrub", backup_set_id: "src/set-1", status: "cancelling" }, 202);
    vi.stubGlobal("fetch", f);
    const got = await httpApi.cancelAssuranceOperation("op/s 1");
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/assurance/operations/op%2Fs%201/cancel");
    expect(c.url).not.toContain("/api/v1/operations");
    expect(c.method).toBe("POST");
    expect(c.headers.get("X-CSRF-Token")).toBe("tok");
    expect(got).toEqual({ operationId: "op/s 1", kind: "scrub", backupSetId: "src/set-1", status: "cancelling" });
  });

  it("getRecoveryTestOutput is a GET with no CSRF token and maps all nine keys", async () => {
    const f = fetchReturning({
      backup_set_id: "src/set-1", recorded: true, operation_id: "op-t", artifact_id: "src/set-1/a", medium: "local",
      state: "failed", recorded_at: "2026-10-09T03:00:00Z", truncated: true,
      records: [{ kind: "output", text: "hi" }, { kind: "truncation", text: "output truncated: 9 bytes dropped" }]
    });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.getRecoveryTestOutput("src", "set-1");
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/backup-sets/src/set-1/recovery-test/output");
    expect(c.method).toBe("GET");
    expect(c.headers.get("X-CSRF-Token")).toBeNull();
    expect(got).toEqual({
      backupSetId: "src/set-1", recorded: true, operationId: "op-t", artifactId: "src/set-1/a", medium: "local", state: "failed",
      recordedAt: "2026-10-09T03:00:00Z", truncated: true,
      records: [{ kind: "output", text: "hi" }, { kind: "truncation", text: "output truncated: 9 bytes dropped" }]
    });
  });

  it("before any test is recorded the records are an empty list, never null or undefined", async () => {
    vi.stubGlobal("fetch", fetchReturning({
      backup_set_id: "src/set-1", recorded: false, operation_id: "", artifact_id: "", medium: "", state: "", recorded_at: "", truncated: false, records: null
    }));
    const got = await httpApi.getRecoveryTestOutput("src", "set-1");
    expect(got.recorded).toBe(false);
    expect(got.records).toEqual([]);
  });

  it("a record kind this build does not know is kept as output, never promoted to the runner's marker", async () => {
    vi.stubGlobal("fetch", fetchReturning({
      backup_set_id: "src/set-1", recorded: true, operation_id: "o", artifact_id: "a", medium: "m", state: "passed", recorded_at: "t", truncated: false,
      records: [{ kind: "note", text: "output truncated: 1 bytes dropped" }]
    }));
    const got = await httpApi.getRecoveryTestOutput("src", "set-1");
    expect(got.records).toEqual([{ kind: "output", text: "output truncated: 1 bytes dropped" }]);
  });

  it("startRecoveryTest carries coalesced through", async () => {
    vi.stubGlobal("fetch", fetchReturning({ backup_set_id: "src/set-1", operation_id: "op-r", coalesced: true }, 202));
    expect(await httpApi.startRecoveryTest("src", "set-1")).toEqual({ backupSetId: "src/set-1", operationId: "op-r", coalesced: true });
  });
});
