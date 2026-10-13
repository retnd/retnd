/**
 * The wire of the Backups list's multi-file recovery (issue #1107, over
 * #1106's `POST /recoveries`): the request body, the CSRF header, and the
 * reading of an application/x-ndjson answer, including a line that arrives in
 * two pieces, which `res.json()` could never have handled.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { httpApi } from "./client";
import type { RecoverManyEvent } from "./contracts";
import { apiErrorOf } from "./failure";

const enc = new TextEncoder();
function streamOf(chunks: string[]) {
  return new ReadableStream<Uint8Array>({
    start(c) { for (const x of chunks) c.enqueue(enc.encode(x)); c.close(); }
  });
}
const totals = { bytes: 0, count: 2, failed: 0, needs_thaw: 0, quarantined: 0, ready: 0, recovered: 2, skipped: 0, unrecoverable: 0 };
const lines = [
  { event: "plan", plan: { items: [], totals, chargeable_bytes: 0, chargeable_count: 0, folder: "/s/x", reserve_bytes: 0, unknown_sizes: 0, target: { kind: "staging" } } },
  { event: "start", index: 1, total: 2, item: { artifact_id: "a/b/one", name: "one", status: "ready" } },
  { event: "item", index: 1, total: 2, done_bytes: 5, total_bytes: 9, item: { artifact_id: "a/b/one", name: "one", status: "recovered", bytes: 5, sha256: "ab", path: "/s/x/a/b/one" } },
  { event: "mystery", index: 9 },
  { event: "result", result: { cancelled: false, folder: "/s/x", items: [], manifest_path: "/s/x/manifest.json", operation_id: "op1", target: { kind: "staging" }, totals } }
];

describe("recoverMany on the wire", () => {
  beforeEach(() => { document.cookie = "retnd_csrf=tok"; window.history.pushState({}, "", "/"); });
  afterEach(() => { vi.unstubAllGlobals(); document.cookie = "retnd_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT"; });

  it("POSTs the ids and one target, reads lines split across chunks, drops a line it does not know, and returns the result", async () => {
    const text = lines.map((l) => JSON.stringify(l)).join("\n") + "\n";
    const cut = text.indexOf("recovered") + 3; // mid-word, mid-line
    const f = vi.fn().mockResolvedValue({
      ok: true, status: 200, headers: new Headers({ "content-type": "application/x-ndjson" }),
      body: streamOf([text.slice(0, cut), text.slice(cut)])
    });
    vi.stubGlobal("fetch", f);
    const seen: RecoverManyEvent[] = [];
    const got = await httpApi.recoverMany(
      { artifactIds: ["a/b/one", "a/b/two"], target: { kind: "staging", path: "drill" }, acknowledged: true },
      (e) => seen.push(e)
    );
    const [url, init] = f.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/recoveries");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("tok");
    expect(JSON.parse(init.body as string)).toEqual({
      artifacts: ["a/b/one", "a/b/two"], target: { kind: "staging", path: "drill" }, acknowledged: true
    });
    expect(seen.map((e) => e.event)).toEqual(["plan", "start", "item", "result"]);
    const item = seen[2];
    expect(item.event === "item" && item.item).toMatchObject({ artifactId: "a/b/one", status: "recovered", sha256: "ab", path: "/s/x/a/b/one" });
    expect(item.event === "item" && item.doneBytes).toBe(5);
    expect(got).toMatchObject({ manifestPath: "/s/x/manifest.json", operationId: "op1", cancelled: false });
  });

  it("a refusal before the stream rejects with the service's code and hands no event over", async () => {
    const f = vi.fn().mockResolvedValue({
      ok: false, status: 409, headers: new Headers({ "content-type": "application/json" }),
      json: async () => ({ error: { code: "RECOVERY_COST_NOT_ACKNOWLEDGED", message: "costs money" } }),
      text: async () => JSON.stringify({ error: { code: "RECOVERY_COST_NOT_ACKNOWLEDGED", message: "costs money" } })
    });
    vi.stubGlobal("fetch", f);
    const seen: RecoverManyEvent[] = [];
    const err = await httpApi
      .recoverMany({ artifactIds: ["a/b/one"], target: { kind: "staging", path: "d" } }, (e) => seen.push(e))
      .then(() => null, (e: unknown) => e);
    expect(apiErrorOf(err)?.code).toBe("RECOVERY_COST_NOT_ACKNOWLEDGED");
    expect(seen).toEqual([]);
  });

  it("omits acknowledged unless it is true: absent is a no", async () => {
    const f = vi.fn().mockResolvedValue({ ok: true, status: 200, headers: new Headers(), body: streamOf([""]) });
    vi.stubGlobal("fetch", f);
    await httpApi.recoverMany({ artifactIds: ["a/b/one"], target: { kind: "storage", medium: "offsite_s3" } }, () => undefined);
    const body = JSON.parse((f.mock.calls[0] as [string, RequestInit])[1].body as string);
    expect(body).toEqual({ artifacts: ["a/b/one"], target: { kind: "storage", medium: "offsite_s3" } });
  });
});
