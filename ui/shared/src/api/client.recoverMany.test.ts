/**
 * What httpApi.recoverMany promises about POST /recoveries (#1106): it reads a
 * stream of JSON lines as they arrive, hands the plan and each file to the
 * caller, resolves with the host's own result line, turns a cancel into a
 * result that says so, and never reports success for a stream that ended
 * without a result.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { httpApi } from "./client";
import type { RecoverManyProgress } from "./contracts";

const item = (name: string, status: string, extra: object = {}) => ({ artifact_id: "src/set/" + name, name, status, ...extra });
const totals = (over: object = {}) => ({ count: 2, ready: 2, recovered: 0, failed: 0, skipped: 0, needs_thaw: 0, quarantined: 0, unrecoverable: 0, bytes: 20, ...over });
const plan = { folder: "out/src/set", target: { kind: "staging", path: "out" }, items: [item("a", "ready", { size_bytes: 10 }), item("b", "ready", { size_bytes: 10 })], totals: totals(), chargeable_count: 0, chargeable_bytes: 0, unknown_sizes: 0, reserve_bytes: 0 };

/** A fetch response whose body arrives in the chunks given, split anywhere. */
function streamOf(chunks: string[], hold?: Promise<void>): Response {
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    async start(c) {
      for (const [i, ch] of chunks.entries()) {
        if (hold && i === chunks.length - 1) await hold;
        c.enqueue(enc.encode(ch));
      }
      c.close();
    }
  });
  return new Response(body, { status: 200, headers: { "content-type": "application/x-ndjson" } });
}
const lines = (...evs: object[]) => evs.map((e) => JSON.stringify(e) + "\n");

afterEach(() => vi.restoreAllMocks());

describe("recoverMany", () => {
  it("reports the plan and each file as they arrive and resolves with the host's result", async () => {
    const result = { operation_id: "op1", folder: "out/src/set", target: plan.target, items: [item("a", "recovered", { bytes: 10, sha256: "aa" }), item("b", "failed", { code: "RECOVERY_LOCAL_COPY_CORRUPT", message: "no" })], totals: totals({ recovered: 1, failed: 1, bytes: 10 }), cancelled: false, manifest_path: "/s/m.json" };
    const all = lines(
      { event: "plan", plan, total_bytes: 20 },
      { event: "start", index: 1, total: 2, item: item("a", "ready") },
      { event: "item", index: 1, total: 2, item: item("a", "recovered", { bytes: 10 }), done_bytes: 10, total_bytes: 20 },
      { event: "result", result }
    ).join("");
    // split mid-line, in the middle of a multi-byte-safe boundary, to prove lines are assembled
    const cut = Math.floor(all.length / 3);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(streamOf([all.slice(0, cut), all.slice(cut, 2 * cut), all.slice(2 * cut)])));
    const seen: RecoverManyProgress[] = [];
    const res = await httpApi.recoverMany({ set: "src/set", target: { kind: "staging", path: "out" } }, { onProgress: (p) => seen.push(p) });
    expect(seen.map((p) => p.kind)).toEqual(["plan", "start", "item"]);
    const last = seen[2];
    expect(last.kind === "item" && last.doneBytes).toBe(10);
    expect(res.totals.recovered).toBe(1);
    expect(res.items[1]).toMatchObject({ status: "failed", code: "RECOVERY_LOCAL_COPY_CORRUPT" });
    expect(res.manifestPath).toBe("/s/m.json");
  });

  it("sends the request as the wire body, preflight false, and only what was chosen", async () => {
    const fetchMock = vi.fn().mockResolvedValue(streamOf(lines({ event: "result", result: { operation_id: "x", folder: "f", target: plan.target, items: [], totals: totals(), cancelled: false } })));
    vi.stubGlobal("fetch", fetchMock);
    await httpApi.recoverMany({ set: "src/set", newestOnly: true, acknowledged: true, target: { kind: "storage", medium: "offsite" } });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/recoveries");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ set: "src/set", newest_only: true, acknowledged: true, target: { kind: "storage", medium: "offsite" } });
  });

  it("a stream that ends without a result is an error, never a success", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(streamOf(lines({ event: "plan", plan, total_bytes: 20 }))));
    await expect(httpApi.recoverMany({ set: "src/set", target: { kind: "staging", path: "out" } })).rejects.toThrow(/without saying how it finished/);
  });

  it("aborting resolves a result marked cancelled, with the files the host had finished and none it had not", async () => {
    const ctl = new AbortController();
    let release!: () => void;
    const hold = new Promise<void>((r) => { release = r; });
    const chunks = lines(
      { event: "plan", plan, total_bytes: 20 },
      { event: "item", index: 1, total: 2, item: item("a", "recovered", { bytes: 10, sha256: "aa" }), done_bytes: 10, total_bytes: 20 },
      { event: "start", index: 2, total: 2, item: item("b", "ready") }
    );
    vi.stubGlobal("fetch", vi.fn().mockImplementation((_u: string, init: RequestInit) => {
      const res = streamOf(chunks, hold);
      init.signal?.addEventListener("abort", () => release());
      return Promise.resolve(res);
    }));
    let items = 0;
    const p = httpApi.recoverMany({ set: "src/set", target: { kind: "staging", path: "out" } }, {
      signal: ctl.signal,
      onProgress: (e) => { if (e.kind === "item") { items++; ctl.abort(); } }
    });
    const res = await p;
    expect(items).toBe(1);
    expect(res.cancelled).toBe(true);
    expect(res.items.find((i) => i.name === "a")?.status).toBe("recovered");
    expect(res.items.find((i) => i.name === "b")).toMatchObject({ status: "skipped", code: "RECOVERY_INTERRUPTED" });
    expect(res.totals.recovered).toBe(1);
    expect(res.manifestPath).toBeUndefined();
  });
});

describe("planRecoverMany", () => {
  it("sends preflight true and maps the refusal member", async () => {
    const body = { ...plan, chargeable_count: 2, chargeable_bytes: 20, refusal: { code: "RECOVERY_COST_NOT_ACKNOWLEDGED", message: "2 of these 2 files are billed" } };
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, headers: new Headers({ "content-type": "application/json" }), json: async () => body });
    vi.stubGlobal("fetch", fetchMock);
    const p = await httpApi.planRecoverMany({ set: "src/set", target: { kind: "staging", path: "out" } });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).preflight).toBe(true);
    expect(p.refusal).toEqual({ code: "RECOVERY_COST_NOT_ACKNOWLEDGED", message: "2 of these 2 files are billed" });
    expect(p.chargeableCount).toBe(2);
    expect(p.items[0]).toMatchObject({ name: "a", status: "ready", sizeBytes: 10 });
  });
});
