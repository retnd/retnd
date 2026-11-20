/**
 * Issue #1589: the card must not touch React state once it has unmounted.
 *
 * The hook failed on tier-destination-picker.test.tsx with an unhandled
 * rejection after teardown. The Settings page mounts this card, the card asks
 * for its list, the mock answers 180ms later, and by then the file's last test
 * had unmounted the page and vitest had torn jsdom down. setList ran anyway.
 * React 18 drops an update to an unmounted component without a warning, but
 * before it gets that far it reads `window.event` to pick a lane, and with no
 * `window` left that read throws. The throw lands inside the .then() the card
 * hands the list to, so the promise `void load()` threw away rejects and
 * nothing is listening.
 *
 * Each case holds a request open, unmounts, and only then lets it answer;
 * test/teardownProbe.ts says why that is the deterministic way to catch it.
 */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { CustomValidator, CustomValidatorList, ValidatorScript } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { CustomValidatorsCard } from "./CustomValidatorsCard";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

describe("CustomValidatorsCard after unmount (#1589)", () => {
  it("ignores a list that answers after the card has gone", async () => {
    const answer = held<CustomValidatorList>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = { ...base, listCustomValidators: () => answer.promise };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    unmount();
    probe.tearDown();
    answer.resolve(list);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a list read that fails after the card has gone", async () => {
    const answer = held<CustomValidatorList>();
    const api = { ...createMockApi("default"), listCustomValidators: () => answer.promise };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a script that arrives after the card has gone", async () => {
    const answer = held<ValidatorScript>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const script = await base.getCustomValidatorScript("billing-dump");
    const api = {
      ...base,
      listCustomValidators: () => Promise.resolve(list),
      getCustomValidatorScript: () => answer.promise
    };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "View script billing-dump" }));
    unmount();
    probe.tearDown();
    answer.resolve(script);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a remove that finishes after the card has gone", async () => {
    const answer = held<CustomValidator>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = {
      ...base,
      listCustomValidators: () => Promise.resolve(list),
      removeCustomValidator: () => answer.promise
    };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Remove billing-dump" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove validator" }));
    unmount();
    probe.tearDown();
    answer.resolve(list.validators.find((v) => v.name === "billing-dump")!);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // The control: the probe is not the thing failing. A card left mounted
  // takes its list as it always did.
  it("still shows a list that answers while the card is there", async () => {
    const answer = held<CustomValidatorList>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = { ...base, listCustomValidators: () => answer.promise };

    render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    answer.resolve(list);
    expect(await screen.findByRole("button", { name: "Remove billing-dump" })).toBeInTheDocument();
  });
});
