/**
 * Issue #1203, review round: an answer that arrives after a newer question
 * was asked must not replace the newer answer (DeclareValidator, Settings card).
 */
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { CustomValidatorList, RetndApi } from "@shared/api/contracts";
import { CustomValidatorsCard } from "./CustomValidatorsCard";
import { DeclareValidator } from "./DeclareValidator";

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

const listWith = async (names: string[], resolution?: CustomValidatorList["resolution"]): Promise<CustomValidatorList> => {
  const base = await createMockApi("validators-none").listCustomValidators();
  return {
    ...base,
    validators: names.map((name) => ({
      name, pattern: "^" + name + "$", backupSetId: "", scope: "global" as const, anchored: true, env: "minimal" as const, scriptFile: name + ".local.sh",
      scriptPresent: true, scriptSha256: "ab", scriptSizeBytes: 1, uploadedAt: "", matchCount: 0, matchSets: [], exampleName: ""
    })),
    ...(resolution ? { resolution } : {})
  };
};

describe("stale list answers", () => {
  it("the Settings card keeps the newest list when an older request answers last", async () => {
    const slow = deferred<CustomValidatorList>();
    const old = { ...createMockApi("validators-none"), listCustomValidators: () => slow.promise } as RetndApi;
    const fresh = { ...createMockApi("validators-none"), listCustomValidators: () => listWith(["fresh-one"]) } as RetndApi;
    const { rerender } = render(<ApiProvider api={old}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    rerender(<ApiProvider api={fresh}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    await waitFor(() => expect(screen.getByText("fresh-one")).toBeInTheDocument());
    slow.resolve(await listWith(["stale-one"]));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText("stale-one")).toBeNull();
    expect(screen.getByText("fresh-one")).toBeInTheDocument();
  });

  it("DeclareValidator shows the resolution of the backup it is on, not an earlier one", async () => {
    const slow = deferred<CustomValidatorList>();
    const api = (id: string) => ({
      ...createMockApi("validators-none"),
      listCustomValidators: (f?: { artifactId?: string }) =>
        f?.artifactId === "s/a/one.txt" ? slow.promise : listWith([], { artifactId: id, validator: "winner-of-two", fallback: "", ambiguous: [] })
    }) as RetndApi;
    const { rerender } = render(<ApiProvider api={api("s/a/one.txt")}><DeclareValidator artifactId="s/a/one.txt" readOnly={false} /></ApiProvider>);
    rerender(<ApiProvider api={api("s/a/two.txt")}><DeclareValidator artifactId="s/a/two.txt" readOnly={false} /></ApiProvider>);
    await waitFor(() => expect(screen.getByText(/runs the validator winner-of-two/)).toBeInTheDocument());
    slow.resolve(await listWith([], { artifactId: "s/a/one.txt", validator: "loser-of-one", fallback: "", ambiguous: [] }));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText(/loser-of-one/)).toBeNull();
    expect(screen.getByText(/winner-of-two/)).toBeInTheDocument();
  });
});
