/**
 * Issue #1111 (settings lane): the built-in structure check's configuration,
 * as the Settings assurance card and the per-set Assurance section show it.
 *
 * Red first. These pin: the `builtin_check` select on both surfaces and its
 * round trip through the PATCH bodies, the `builtin` mode option, the two
 * refusals the server gives (builtin mode with the check off, and a `retnd-`
 * validator id) said in the server's own words before the request is sent, the
 * CommandEcho agreeing with core/cliecho, and help copy whose claims are
 * stated from the engine.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { httpApi } from "@shared/api/client";
import { createMockApi } from "@shared/api/mock";
import type { AssuranceSettings, BackupSetAssurance, RetndApi } from "@shared/api/contracts";
import { AssuranceSection } from "@shared/assurance/AssuranceSection";
import { AssuranceSettingsCard } from "@shared/assurance/AssuranceSettingsCard";
import { withHttpAssurance } from "@shared/assurance/realAssurance";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { resetGraphForTests } from "@shared/state/graph";
import { TOOLTIPS } from "@shared/tooltips/tooltips";
import goConfigSrc from "../../../../core/internal/config/assurance.go?raw";
import goEchoTestSrc from "../../../../core/cliecho/builtin_check_1111_test.go?raw";

const SET = "production/postgres-primary";
const T = "2026-10-09T03:00:00Z";

const settings = (over: Partial<AssuranceSettings["recoveryTest"]> = {}): AssuranceSettings => ({
  configured: true,
  scrub: { cadenceSeconds: 604800, strategy: "auto" },
  recoveryTest: { mode: "off", cadenceSeconds: 2592000, validatorId: "", builtinCheck: "structure", ...over },
  stagingRoot: "/var/lib/retnd/recovery-staging", stagingTtlSeconds: 86400, maxConcurrentFullReads: 2, maxPerDestination: 1, hostKnobs: []
});
const policy = (over: Partial<BackupSetAssurance["recoveryTest"]> = {}, isOverride = true): BackupSetAssurance => ({
  backupSetId: SET, isOverride,
  scrub: { cadenceSeconds: 604800, strategy: "auto" },
  recoveryTest: { mode: "off", cadenceSeconds: 2592000, validatorId: "", builtinCheck: "structure", ...over }
});

function renderCard(over: Partial<RetndApi> = {}) {
  const root = { ...createMockApi("default"), ...over } as RetndApi;
  render(<MemoryRouter><ApiProvider api={root}><AssuranceSettingsCard readOnly={false} /></ApiProvider></MemoryRouter>);
}
const noLive = { getLiveActivity: vi.fn().mockResolvedValue({ observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] }) };
function renderSection(over: Partial<RetndApi> = {}) {
  const root = { ...createMockApi("assurance-unavailable"), ...noLive, ...over } as RetndApi;
  render(<MemoryRouter><ApiProvider api={withHttpAssurance(root)}><AssuranceSection setId={SET} readOnly={false} /></ApiProvider></MemoryRouter>);
}
const save = () => screen.getByRole("button", { name: /^Save/ }) as HTMLButtonElement;

describe("#1111 the wire: builtin_check round trips", () => {
  afterEach(() => vi.restoreAllMocks());
  const okJson = (body: unknown) => vi.fn().mockResolvedValue({ ok: true, status: 200, headers: new Headers(), json: async () => body });

  it("reads builtin_check, effective_check and its reason off the settings response", async () => {
    vi.stubGlobal("fetch", okJson({
      configured: true, scrub: {}, recovery_test: { mode: "builtin", builtin_check: "off", effective_check: "none", effective_check_reason: "the built-in check is off" }
    }));
    const got = await httpApi.getAssuranceSettings();
    expect(got.recoveryTest).toMatchObject({ builtinCheck: "off", effectiveCheck: "none", effectiveCheckReason: "the built-in check is off" });
  });

  it("sends recovery_test_builtin_check on both PATCH bodies, empty included (empty clears it)", async () => {
    const f = okJson({ configured: true });
    vi.stubGlobal("fetch", f);
    await httpApi.patchAssuranceSettings({ recoveryTestBuiltinCheck: "off" });
    await httpApi.patchBackupSetAssurance("production", "postgres-primary", { recoveryTestBuiltinCheck: "" });
    const bodies = f.mock.calls.map((c) => JSON.parse(String((c[1] as RequestInit).body)));
    expect(bodies[0]).toEqual({ recovery_test_builtin_check: "off" });
    expect(bodies[1]).toEqual({ recovery_test_builtin_check: "" });
  });
});

describe("#1111 Settings assurance card", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  it("has a 'Built-in check when no validator' select showing the resolved value", async () => {
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings({ builtinCheck: "off" })) });
    const sel = (await screen.findByLabelText("Built-in check when no validator")) as HTMLSelectElement;
    expect(sel.value).toBe("off");
    expect([...sel.options].map((o) => o.value)).toEqual(["structure", "off"]);
  });

  it("offers builtin beside off and required in the mode select", async () => {
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings()) });
    const mode = (await screen.findByLabelText("Recovery test")) as HTMLSelectElement;
    expect([...mode.options].map((o) => o.value)).toEqual(["off", "builtin", "required"]);
  });

  it("saves a changed choice as recoveryTestBuiltinCheck and nothing else", async () => {
    const patch = vi.fn().mockImplementation(async (p) => settings({ builtinCheck: p.recoveryTestBuiltinCheck }));
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings()), patchAssuranceSettings: patch });
    const sel = await screen.findByLabelText("Built-in check when no validator");
    await userEvent.selectOptions(sel, "off");
    await userEvent.click(save());
    await waitFor(() => expect(patch).toHaveBeenCalledWith({ recoveryTestBuiltinCheck: "off" }));
    expect(await screen.findByRole("status")).toHaveProperty("textContent", "Saved");
    expect((screen.getByLabelText("Built-in check when no validator") as HTMLSelectElement).value).toBe("off");
  });

  it("refuses mode builtin with the check off, in the server's words, and will not save it", async () => {
    const patch = vi.fn();
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings()), patchAssuranceSettings: patch });
    await userEvent.selectOptions(await screen.findByLabelText("Recovery test"), "builtin");
    await userEvent.selectOptions(screen.getByLabelText("Built-in check when no validator"), "off");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("mode builtin runs the built-in structure check, so builtin_check: off refuses it");
    expect(alert.textContent).toContain("set builtin_check to structure or choose another mode");
    // both keys are named, as the loader does
    expect(alert.textContent).toMatch(/builtin_check/);
    expect(alert.textContent).toMatch(/mode builtin/);
    expect(save().disabled).toBe(true);
    expect(patch).not.toHaveBeenCalled();
  });

  it("the refusal text is the loader's own sentence", () => {
    expect(goConfigSrc).toContain("mode %s runs the built-in structure check, so builtin_check: %s refuses it; set builtin_check to %s or choose another mode");
  });

  it("refuses a validator id beginning retnd-, in the server's words, and will not save it", async () => {
    const patch = vi.fn();
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings({ mode: "required", validatorId: "pg-check" })), patchAssuranceSettings: patch });
    const v = await screen.findByLabelText("Validator");
    await userEvent.clear(v);
    await userEvent.type(v, "retnd-foo");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain('"retnd-foo" is not usable: ids beginning retnd- are the product\'s own built-in checks');
    expect(save().disabled).toBe(true);
    expect(patch).not.toHaveBeenCalled();
  });

  it("the CommandEcho prints --recovery-test-builtin-check as core/cliecho does", async () => {
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings()) });
    await userEvent.selectOptions(await screen.findByLabelText("Built-in check when no validator"), "off");
    expect(document.body.textContent).toContain("retnd settings assurance patch --recovery-test-builtin-check off");
    // pinned to the Go side's own expectation, not just to this file
    expect(goEchoTestSrc).toContain("settings assurance patch --recovery-test-builtin-check off");
  });

  it("says what the check does without claiming a validator's pass", async () => {
    renderCard({ getAssuranceSettings: vi.fn().mockResolvedValue(settings()) });
    await screen.findByLabelText("Built-in check when no validator");
    const text = document.body.textContent ?? "";
    expect(text).toMatch(/never satisfies/i);
    expect(text).not.toMatch(/Recovery test passed/);
  });
});

describe("#1111 per-set Assurance section", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  const own = async (over: Partial<BackupSetAssurance["recoveryTest"]> = {}, extra: Partial<RetndApi> = {}) => {
    renderSection({ getBackupSetAssurance: vi.fn().mockResolvedValue(policy(over)), ...extra });
    return (await screen.findByLabelText("Built-in check when no validator")) as HTMLSelectElement;
  };

  it("shows the select in an own policy with the set's resolved value", async () => {
    const sel = await own({ builtinCheck: "off" });
    expect(sel.value).toBe("off");
  });

  it("offers builtin in the mode select", async () => {
    await own();
    const mode = screen.getByLabelText("Recovery test") as HTMLSelectElement;
    expect([...mode.options].map((o) => o.value)).toEqual(["off", "builtin", "required", "custom"]);
  });

  it("saves the pinned builtin_check with the rest of the own policy", async () => {
    const patch = vi.fn().mockResolvedValue(policy({ builtinCheck: "off" }));
    const sel = await own({}, { patchBackupSetAssurance: patch });
    await userEvent.selectOptions(sel, "off");
    await userEvent.click(save());
    await waitFor(() => expect(patch).toHaveBeenCalled());
    expect(patch.mock.calls[0][2]).toMatchObject({ recoveryTestBuiltinCheck: "off" });
  });

  it("a builtin mode sends no validator, because the loader refuses one beside it", async () => {
    const patch = vi.fn().mockResolvedValue(policy({ mode: "builtin" }));
    await own({}, { patchBackupSetAssurance: patch });
    await userEvent.selectOptions(screen.getByLabelText("Recovery test"), "builtin");
    expect(screen.queryByLabelText("Validator")).toBeNull();
    await userEvent.click(save());
    await waitFor(() => expect(patch).toHaveBeenCalled());
    expect(patch.mock.calls[0][2]).toMatchObject({ recoveryTestMode: "builtin", recoveryTestValidatorId: "" });
  });

  it("refuses builtin mode with the check off, and a retnd- validator id, in the server's words", async () => {
    await own();
    await userEvent.selectOptions(screen.getByLabelText("Recovery test"), "builtin");
    await userEvent.selectOptions(screen.getByLabelText("Built-in check when no validator"), "off");
    expect((await screen.findAllByRole("alert")).map((a) => a.textContent).join(" ")).toContain("mode builtin runs the built-in structure check, so builtin_check: off refuses it");
    expect(save().disabled).toBe(true);
    await userEvent.selectOptions(screen.getByLabelText("Built-in check when no validator"), "structure");
    await userEvent.selectOptions(screen.getByLabelText("Recovery test"), "required");
    const v = screen.getByLabelText("Validator");
    await userEvent.type(v, "retnd-x");
    expect((await screen.findAllByRole("alert")).map((a) => a.textContent).join(" ")).toContain("ids beginning retnd- are the product's own built-in checks");
    expect(save().disabled).toBe(true);
  });

  it("the CommandEcho prints the per-set flag as core/cliecho does", async () => {
    const sel = await own();
    await userEvent.selectOptions(sel, "off");
    expect(document.body.textContent).toContain("retnd backup-set assurance " + SET + " --recovery-test-builtin-check off");
    expect(goEchoTestSrc).toContain("backup-set assurance api-server/var-backups --recovery-test-builtin-check structure");
  });

  it("Run recovery test stays enabled when the engine says the built-in check applies", async () => {
    renderSection({ getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: false, effectiveCheck: "builtin" }, false)) });
    const run = (await screen.findByRole("button", { name: "Run recovery test" })) as HTMLButtonElement;
    expect(run.disabled).toBe(false);
    expect(screen.queryByText(/a test can only be refused/i)).toBeNull();
  });

  it("with no check at all it stays disabled and gives the engine's reason", async () => {
    renderSection({ getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: false, effectiveCheck: "none", effectiveCheckReason: "the built-in check has no reader for this file type (.dump); nothing was read" }, false)) });
    const run = (await screen.findByRole("button", { name: "Run recovery test" })) as HTMLButtonElement;
    expect(run.disabled).toBe(true);
    expect(screen.getByText(/no reader for this file type \(\.dump\)/)).toBeTruthy();
  });
});

describe("#1111 help copy states the effect from the code", () => {
  const keys = ["assuranceBuiltinCheck", "assuranceSetBuiltinCheck"] as const;
  for (const k of keys) {
    it(k + " has three parts and states the engine's rules", () => {
      const h = (FIELD_HELP as Record<string, { what: string; example: string; effect: string }>)[k];
      expect(h, k).toBeTruthy();
      expect(h.what.length).toBeGreaterThan(20);
      expect(h.example).toMatch(/structure|off/);
      for (const claim of [/manual|Run recovery test|retnd recovery-test/i, /mode is builtin|mode builtin/i, /never satisf/i, /before (?:anything is )?staged|nothing is staged/i, /gzip, zstd/i]) {
        expect(h.effect, k + " must say " + claim).toMatch(claim);
      }
    });
  }
  it("the mode help names builtin", () => {
    expect(FIELD_HELP.assuranceRecoveryTestMode.what + FIELD_HELP.assuranceRecoveryTestMode.effect).toMatch(/builtin/);
  });
  it("tooltips exist for the new controls and say what the built-in check will not claim", () => {
    for (const id of ["assurance.settings.builtinCheck", "assurance.set.builtinCheck"] as const) {
      const t = (TOOLTIPS as Record<string, { label: string; html: string }>)[id];
      expect(t, id).toBeTruthy();
      expect(t.html).toMatch(/not (?:prove|satisf)|never satisf/i);
    }
  });
  it("control: the server's retnd- refusal sentence is still in the Go source this file is pinned to", () => {
    expect(goConfigSrc).toContain("ids beginning %s are the product's own built-in checks");
  });
});
