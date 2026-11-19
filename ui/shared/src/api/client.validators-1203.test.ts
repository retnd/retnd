/**
 * The wire shapes of the custom recovery-test validator routes (issue #1203):
 * which path and verb each method calls, that the password rides a header on
 * the two writes and never the body, that a remove carries no password, and
 * what an absent field becomes on the way into a domain type.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { STEP_UP_HEADER, httpApi, rememberInsecureTransportRequired } from "./client";

function fetchReturning(body: unknown, status = 200) {
  return vi.fn().mockResolvedValue({ ok: status < 400, status, headers: new Headers(), json: async () => body });
}
const lastCall = (f: ReturnType<typeof vi.fn>) => {
  const [url, init] = f.mock.calls[f.mock.calls.length - 1] as [string, RequestInit | undefined];
  return { url, method: init?.method ?? "GET", headers: new Headers(init?.headers), body: init?.body ? JSON.parse(String(init.body)) : undefined };
};

const wireValidator = {
  anchored: true, backup_set_id: "", env: "minimal", example_name: "a-1.txt", match_count: 1, match_sets: ["src/set-1"], name: "v",
  pattern: "^a-\\d$", scope: "global", script_file: "v.local.sh", script_present: true, script_sha256: "ab", script_size_bytes: 12,
  uploaded_at: "2026-10-16T00:00:00Z"
};

describe("the custom validator routes on the wire", () => {
  beforeEach(() => {
    document.cookie = "retnd_csrf=tok";
    window.history.pushState({}, "", "/");
    // The capabilities said this connection needs no extra question before a
    // password is sent (that path has its own tests in client.test.ts).
    rememberInsecureTransportRequired(false);
  });
  afterEach(() => { vi.unstubAllGlobals(); document.cookie = "retnd_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT"; });

  it("lists, with a backup set and an artifact in the query when asked about them", async () => {
    const f = fetchReturning({
      available: true, directory: "/d", max_pattern_bytes: 512, max_script_bytes: 262144, max_validators: 64,
      runner_configured: true, unavailable_reason: "", validators: [wireValidator], workflow_root: "/w",
      resolution: { ambiguous: [], artifact_id: "src/set-1/a-1.txt", fallback: "", validator: "v" }
    });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.listCustomValidators({ backupSetId: "src/set-1", artifactId: "src/set-1/a-1.txt" });
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/assurance/validators?backup_set=src%2Fset-1&artifact=src%2Fset-1%2Fa-1.txt");
    expect(c.method).toBe("GET");
    expect(c.headers.get("X-CSRF-Token")).toBeNull();
    expect(got.validators[0]).toMatchObject({ name: "v", backupSetId: "", scope: "global", matchCount: 1, scriptPresent: true });
    expect(got.resolution).toEqual({ artifactId: "src/set-1/a-1.txt", validator: "v", fallback: "", ambiguous: [] });
  });

  it("lists with no query at all when asked about nothing", async () => {
    const f = fetchReturning({ available: false, unavailable_reason: "no root", validators: null });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.listCustomValidators();
    expect(lastCall(f).url).toBe("/api/v1/assurance/validators");
    expect(got.validators).toEqual([]);
    expect(got.available).toBe(false);
  });

  it("asks the engine to judge a pattern with POST, and leaves out what it was not given", async () => {
    const f = fetchReturning({ valid: false, error: "bad", anchored: false, match_count: 0, match_sets: null, sample: null, conflicts: null });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.previewCustomValidator({ pattern: "^(?=x)$" });
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/assurance/validator-preview");
    expect(c.method).toBe("POST");
    expect(c.headers.get("X-CSRF-Token")).toBe("tok");
    expect(c.body).toEqual({ pattern: "^(?=x)$" });
    expect(got).toMatchObject({ valid: false, error: "bad", matchSets: [], sample: [], conflicts: [] });
  });

  it("hands the caller's abort signal to the preview request", async () => {
    const f = fetchReturning({ valid: true, error: "", anchored: true, match_count: 0, match_sets: null, sample: null, conflicts: null });
    vi.stubGlobal("fetch", f);
    const ac = new AbortController();
    await httpApi.previewCustomValidator({ pattern: "^a$" }, { signal: ac.signal });
    const init = f.mock.calls[f.mock.calls.length - 1][1] as RequestInit;
    expect(init.signal).toBeInstanceOf(AbortSignal);
    ac.abort();
    expect(init.signal?.aborted).toBe(true);
  });

  it("uploads with the password in a header only, and no backup set for a global validator", async () => {
    const f = fetchReturning(wireValidator, 201);
    vi.stubGlobal("fetch", f);
    await httpApi.createCustomValidator({ name: "v", pattern: "^a$", script: "#!/bin/sh\n" }, { stepUpPassword: "hunter2" });
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/assurance/validators");
    expect(c.method).toBe("POST");
    expect(c.headers.get(STEP_UP_HEADER)).toBe("hunter2");
    expect(c.headers.get("X-CSRF-Token")).toBe("tok");
    expect(c.body).toEqual({ name: "v", pattern: "^a$", script: "#!/bin/sh\n" });
    expect(JSON.stringify(c.body)).not.toContain("hunter2");
  });

  it("sends no password header when none was asked for", async () => {
    const f = fetchReturning(wireValidator, 201);
    vi.stubGlobal("fetch", f);
    await httpApi.createCustomValidator({ name: "v", pattern: "^a$", backupSetId: "src/set-1", script: "x" });
    const c = lastCall(f);
    expect(c.headers.get(STEP_UP_HEADER)).toBeNull();
    expect(c.body.backup_set_id).toBe("src/set-1");
  });

  // #1213 made the PUT a true replace: the pattern and the scope are always
  // the new ones, and the script is either sent or kept by its hash.
  it("edits with PUT on the escaped name, and keeps the script by its hash, with the scope spelled out", async () => {
    const f = fetchReturning(wireValidator);
    vi.stubGlobal("fetch", f);
    await httpApi.updateCustomValidator("a b", { pattern: "^b$", keepScriptSha256: "ab12" }, { stepUpPassword: "pw" });
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/assurance/validators/a%20b");
    expect(c.method).toBe("PUT");
    expect(c.headers.get(STEP_UP_HEADER)).toBe("pw");
    expect(c.body).toEqual({ pattern: "^b$", backup_set_id: "", script_sha256: "ab12" });
  });

  it("edits with a new script and a set, and sends the hash of nothing", async () => {
    const f = fetchReturning(wireValidator);
    vi.stubGlobal("fetch", f);
    await httpApi.updateCustomValidator("v", { pattern: "^b$", backupSetId: "src/set-1", script: "#!/bin/sh\n" });
    expect(lastCall(f).body).toEqual({ pattern: "^b$", backup_set_id: "src/set-1", script: "#!/bin/sh\n" });
  });

  it("reads the ambiguities and the refused directory a list now carries", async () => {
    const f = fetchReturning({
      available: true, directory: "/d", directory_refused: "writable by another account", max_pattern_bytes: 512, max_script_bytes: 262144,
      max_validators: 64, runner_configured: true, unavailable_reason: "", validators: [], workflow_root: "/w",
      ambiguities: [{ backup_set_id: "src/set-1", example: "src/set-1/a-1.txt", validators: ["x", "y"] }]
    });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.listCustomValidators();
    expect(got.directoryRefused).toBe("writable by another account");
    expect(got.ambiguities).toEqual([{ backupSetId: "src/set-1", example: "src/set-1/a-1.txt", validators: ["x", "y"] }]);
  });

  it("reads an engine that does not send them as having none", async () => {
    const f = fetchReturning({ available: true, validators: null });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.listCustomValidators();
    expect(got.directoryRefused).toBe("");
    expect(got.ambiguities).toEqual([]);
  });

  it("removes with DELETE, a CSRF token and no password", async () => {
    const f = fetchReturning(wireValidator);
    vi.stubGlobal("fetch", f);
    await httpApi.removeCustomValidator("v");
    const c = lastCall(f);
    expect(c.url).toBe("/api/v1/assurance/validators/v");
    expect(c.method).toBe("DELETE");
    expect(c.headers.get("X-CSRF-Token")).toBe("tok");
    expect(c.headers.get(STEP_UP_HEADER)).toBeNull();
  });

  it("reads a script back, and the default one, into the page's own names", async () => {
    const f = fetchReturning({ content: "#!/bin/sh\n", file_name: "v.local.sh", sha256: "cd", size_bytes: 10 });
    vi.stubGlobal("fetch", f);
    expect(await httpApi.getCustomValidatorScript("v")).toEqual({ content: "#!/bin/sh\n", fileName: "v.local.sh", sha256: "cd", sizeBytes: 10 });
    expect(lastCall(f).url).toBe("/api/v1/assurance/validators/v/script");
    await httpApi.getDefaultValidatorScript();
    expect(lastCall(f).url).toBe("/api/v1/assurance/default-validator-script");
  });

  it("maps the suggested patterns", async () => {
    const f = fetchReturning({ backup_set_id: "", patterns: [{ example: "a-1.txt", match_count: 3, match_sets: ["s/t"], pattern: "^a-\\d{1}\\.txt$", validator: "" }] });
    vi.stubGlobal("fetch", f);
    const got = await httpApi.listCustomValidatorPatterns();
    expect(lastCall(f).url).toBe("/api/v1/assurance/validator-patterns");
    expect(got.patterns[0]).toEqual({ pattern: "^a-\\d{1}\\.txt$", example: "a-1.txt", matchCount: 3, matchSets: ["s/t"], validator: "" });
  });
});
