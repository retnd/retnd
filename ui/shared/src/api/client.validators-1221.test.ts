import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { httpApi, rememberInsecureTransportRequired } from "./client";
import { createMockApi } from "./mock";

// #1221: a validator's env (minimal or workflow) is part of the whole
// replacement an edit sends, and reaches the wire only when the caller has it.

const wireValidator = {
  anchored: true, backup_set_id: "", env: "workflow", example_name: "a-1.txt", match_count: 1, match_sets: ["src/set-1"], name: "v",
  pattern: "^a-\\d$", scope: "global", script_file: "v.local.sh", script_present: true, script_sha256: "ab", script_size_bytes: 12,
  uploaded_at: "2026-10-16T00:00:00Z"
};

function fetchReturning(body: unknown, status = 200) {
  return vi.fn().mockResolvedValue({ ok: status < 400, status, headers: new Headers(), json: async () => body });
}
const lastCall = (f: ReturnType<typeof vi.fn>) => {
  const [url, init] = f.mock.calls[f.mock.calls.length - 1] as [string, RequestInit | undefined];
  return { url, body: init?.body ? JSON.parse(String(init.body)) : undefined };
};

describe("validator env on the wire (#1221)", () => {
  beforeEach(() => {
    document.cookie = "retnd_csrf=tok";
    window.history.pushState({}, "", "/");
    rememberInsecureTransportRequired(false);
  });
  afterEach(() => { vi.unstubAllGlobals(); });

  it("reads env off a validator", async () => {
    vi.stubGlobal("fetch", fetchReturning(wireValidator));
    const got = await httpApi.removeCustomValidator("v");
    expect(got.env).toBe("workflow");
  });

  it("sends the env an edit was given, in the whole replacement", async () => {
    const f = fetchReturning(wireValidator);
    vi.stubGlobal("fetch", f);
    await httpApi.updateCustomValidator("v", { pattern: "^b$", keepScriptSha256: "ab12", env: "workflow" }, { stepUpPassword: "pw" });
    expect(lastCall(f).body).toEqual({ pattern: "^b$", backup_set_id: "", script_sha256: "ab12", env: "workflow" });
  });

  it("sends no env when the caller has none, and an add says none unless asked", async () => {
    const f = fetchReturning(wireValidator);
    vi.stubGlobal("fetch", f);
    await httpApi.updateCustomValidator("v", { pattern: "^b$", keepScriptSha256: "ab12" });
    expect(lastCall(f).body).not.toHaveProperty("env");
    await httpApi.createCustomValidator({ name: "v", pattern: "^a$", script: "x" });
    expect(lastCall(f).body).not.toHaveProperty("env");
  });
});

describe("the in-process mock keeps env like the engine does (#1221)", () => {
  it("an add is minimal, an edit that sends env keeps it and one that sends none is minimal again", async () => {
    const api = createMockApi("validators-none");
    const made = await api.createCustomValidator({ name: "w", pattern: "^w", script: "#!/bin/sh\n" });
    expect(made.env).toBe("minimal");
    const wide = await api.updateCustomValidator("w", { pattern: "^w", script: "#!/bin/sh\n", env: "workflow" });
    expect(wide.env).toBe("workflow");
    const kept = await api.updateCustomValidator("w", { pattern: "^w", keepScriptSha256: wide.scriptSha256, env: "workflow" });
    expect(kept.env).toBe("workflow");
    const reset = await api.updateCustomValidator("w", { pattern: "^w", keepScriptSha256: kept.scriptSha256 });
    expect(reset.env).toBe("minimal");
  });
});
