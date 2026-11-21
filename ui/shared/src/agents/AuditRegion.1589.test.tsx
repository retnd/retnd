/**
 * Issue #1589 on the agents page: the audit region's first-page read and
 * Show older both set state on answers that arrived after the region had
 * unmounted. test/teardownProbe.ts says why that fails a run, and why
 * holding the answer is the way to catch it.
 */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import type { AgentAuditRow, AgentToken } from "@shared/types/agents";
import { AuditRegion } from "./AuditRegion";

type Page = { rows: AgentAuditRow[]; nextCursor: string | null };
const row = (seq: number, tool: string): AgentAuditRow =>
  ({ seq, tool, tokenId: "t1", tokenName: "drill", at: "2026-11-12T10:00:00Z", outcome: "ok", correlationId: "c" + seq }) as unknown as AgentAuditRow;
const token = { id: "t1", name: "drill" } as unknown as AgentToken;

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

describe("AuditRegion after unmount (#1589)", () => {
  it("ignores a first page that answers after it has gone", async () => {
    const answer = held<Page>();
    const api = { listAgentAudit: () => answer.promise } as unknown as RetndApi;

    const { unmount } = render(<ApiProvider api={api}><AuditRegion tokens={[token]} tools={[]} revision={0} /></ApiProvider>);
    unmount();
    probe.tearDown();
    answer.resolve({ rows: [row(1, "late_tool")], nextCursor: null });
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a first page that fails after it has gone", async () => {
    const answer = held<Page>();
    const api = { listAgentAudit: () => answer.promise } as unknown as RetndApi;

    const { unmount } = render(<ApiProvider api={api}><AuditRegion tokens={[token]} tools={[]} revision={0} /></ApiProvider>);
    unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores an older page that answers after it has gone", async () => {
    const older = held<Page>();
    const api = {
      listAgentAudit: (q: { cursor?: string }) =>
        q.cursor ? older.promise : Promise.resolve({ rows: [row(10, "first_page_tool")], nextCursor: "cur-1" })
    } as unknown as RetndApi;

    const { unmount } = render(<ApiProvider api={api}><AuditRegion tokens={[token]} tools={[]} revision={0} /></ApiProvider>);
    await screen.findByText("first_page_tool");
    fireEvent.click(screen.getByRole("button", { name: /^Audit/ }));
    fireEvent.click(screen.getByRole("button", { name: "Show older" }));
    unmount();
    probe.tearDown();
    older.resolve({ rows: [row(5, "older_tool")], nextCursor: null });
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });
});
