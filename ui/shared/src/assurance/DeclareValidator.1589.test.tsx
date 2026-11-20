/**
 * Issue #1589, the same hole on a backup's page: DeclareValidator reads the
 * validator list the way the Settings card does, and set state on an answer
 * that arrived after it had unmounted. test/teardownProbe.ts says why that
 * fails a run, and why holding the answer is the way to catch it.
 */
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { CustomValidatorList } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { DeclareValidator } from "./DeclareValidator";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

describe("DeclareValidator after unmount (#1589)", () => {
  it("ignores a list that answers after it has gone", async () => {
    const answer = held<CustomValidatorList>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = { ...base, listCustomValidators: () => answer.promise };

    const { unmount } = render(<ApiProvider api={api}><DeclareValidator setId="production/billing-mysql" readOnly={false} /></ApiProvider>);
    unmount();
    probe.tearDown();
    answer.resolve(list);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a list read that fails after it has gone", async () => {
    const answer = held<CustomValidatorList>();
    const api = { ...createMockApi("default"), listCustomValidators: () => answer.promise };

    const { unmount } = render(<ApiProvider api={api}><DeclareValidator setId="production/billing-mysql" readOnly={false} /></ApiProvider>);
    unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });
});
