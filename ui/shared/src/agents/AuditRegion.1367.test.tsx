import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import type { AgentAuditRow, AgentToken } from "@shared/types/agents";
import { AuditRegion } from "./AuditRegion";

/**
 * Show older ignores a page that belongs to an earlier filter (#1367, low).
 * The cursor Show older sends was issued under one filter; if the operator
 * changes the filter while that request is in flight, its answer is a page of
 * the wrong list and must not be appended to the new one.
 */
const row = (seq: number, tool: string): AgentAuditRow =>
  ({ seq, tool, tokenId: "t1", tokenName: "drill", at: "2026-11-12T10:00:00Z", outcome: "ok", correlationId: "c" + seq }) as unknown as AgentAuditRow;

const token = { id: "t1", name: "drill" } as unknown as AgentToken;
const other = { id: "t2", name: "other" } as unknown as AgentToken;

describe("AuditRegion Show older against a changed filter (#1367)", () => {
  it("drops the older page when the filter changed while it was in flight", async () => {
    let release: (v: { rows: AgentAuditRow[]; nextCursor: string | null }) => void = () => undefined;
    const listAgentAudit = vi.fn(async (q: { tokenId?: string; cursor?: string }) => {
      if (q.cursor) return new Promise<{ rows: AgentAuditRow[]; nextCursor: string | null }>((res) => { release = res; });
      if (q.tokenId === "t2") return { rows: [row(20, "filtered_tool")], nextCursor: null };
      return { rows: [row(10, "first_page_tool")], nextCursor: "cur-1" };
    });
    const api = { listAgentAudit } as unknown as RetndApi;
    const user = userEvent.setup();
    render(
      <ApiProvider api={api}>
        <AuditRegion tokens={[token, other]} tools={[]} revision={0} />
      </ApiProvider>
    );
    await screen.findByText("first_page_tool");
    await user.click(screen.getByRole("button", { name: "Show older" }));
    expect(listAgentAudit).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "cur-1" }));

    await user.selectOptions(screen.getByLabelText("Token", { selector: "select" }), "t2");
    await screen.findByText("filtered_tool");

    await act(async () => { release({ rows: [row(5, "stale_older_tool")], nextCursor: "cur-2" }); });

    expect(screen.queryByText("stale_older_tool")).toBeNull();
    expect(screen.getByText("filtered_tool")).toBeInTheDocument();
    // The filtered list has no further page, so there is nothing to ask for.
    expect(within(document.body).queryByRole("button", { name: "Show older" })).toBeNull();
  });
});
