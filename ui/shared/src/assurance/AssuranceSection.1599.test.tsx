/**
 * Issue #1599: the backup set's assurance section reads its policy when it
 * mounts, and that read is the #1589 shape exactly: the page goes (or the test
 * ends) while it's out, the answer lands, and refresh() sets the policy, the
 * newest copy and the due list on a section that has gone. The mount effect's
 * own `live` flag only covered the two setters it calls itself, not the ones
 * inside refresh().
 *
 * Each case holds the read, unmounts, tears down and only then answers; see
 * test/teardownProbe.ts.
 */
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { AssuranceDue, BackupSetAssurance, RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { AssuranceSection } from "./AssuranceSection";

const SET = "production/postgres-primary";
const policy: BackupSetAssurance = {
  backupSetId: SET, isOverride: false,
  scrub: { cadenceSeconds: 604800, strategy: "auto" },
  recoveryTest: { mode: "off", cadenceSeconds: 0, validatorId: "" }
};

let probe: TeardownProbe;
beforeEach(() => { resetGraphForTests(); probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

function renderSection(over: Partial<RetndApi>) {
  const root = { ...createMockApi("assurance-unavailable"), ...over } as RetndApi;
  return render(
    <MemoryRouter>
      <ApiProvider api={root}>
        <AssuranceSection setId={SET} readOnly={false} />
      </ApiProvider>
    </MemoryRouter>
  );
}

describe("AssuranceSection after unmount (#1599)", () => {
  it("ignores a policy that answers after the section has gone", async () => {
    const answer = held<BackupSetAssurance>();
    const { unmount } = renderSection({
      getBackupSetAssurance: () => answer.promise,
      listAssuranceDue: () => Promise.resolve({ items: [] } as unknown as AssuranceDue)
    });
    unmount();
    probe.tearDown();
    answer.resolve(policy);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a due list that answers after the section has gone", async () => {
    const due = held<AssuranceDue>();
    let asked!: () => void;
    const reached = new Promise<void>((r) => { asked = r; });
    const { unmount } = renderSection({
      getBackupSetAssurance: () => Promise.resolve(policy),
      listAssuranceDue: () => { asked(); return due.promise; }
    });
    await reached;
    unmount();
    probe.tearDown();
    due.reject(new Error("the engine went away"));
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("still shows the policy while it's mounted", async () => {
    renderSection({
      getBackupSetAssurance: () => Promise.resolve(policy),
      listAssuranceDue: () => Promise.resolve({ items: [] } as unknown as AssuranceDue)
    });
    expect(await screen.findByRole("radio", { name: /Follow the deployment/ })).toBeChecked();
  });
});
