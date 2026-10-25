/**
 * Issue #1246, review round: `directory` on the wire is relative to the
 * workflow root (that is what the engine sends), and where an upload lands is
 * the engine's absolute `scriptsDir`. The card reads that one field, so the
 * shape the real engine sends is the shape every test here exercises.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { CustomValidatorsCard } from "./CustomValidatorsCard";

describe("where validator scripts are kept (#1246)", () => {
  it("the mock sends directory the way the engine does, relative to the root", async () => {
    const list = await createMockApi("validators-default-root").listCustomValidators();
    expect(list.directory).toBe("recovery-test-validators");
    expect(list.scriptsDir).toBe("/workflows/recovery-test-validators");
  });

  it("the card shows the engine's scriptsDir and does not join paths itself", async () => {
    const base = createMockApi("validators-none");
    const list = await base.listCustomValidators();
    const api = { ...base, listCustomValidators: () => Promise.resolve({ ...list, workflowRoot: "/a", directory: "recovery-test-validators", scriptsDir: "/mnt/b/recovery-test-validators" }) };
    render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    const where = await screen.findByText("/mnt/b/recovery-test-validators");
    expect(where).toBeInTheDocument();
  });
});
