/**
 * Issue #1290: Encrypting and Decrypt-verifying as progress stages.
 *
 * Written by reading and not run. What is asserted: the two stages are known
 * to the shared types, they show on the checklist only for an encrypted copy
 * (a plaintext transfer never grows two rows it cannot reach), they carry the
 * same byte readout as a transfer, and the decrypt-verify dialog's decrypt row
 * can say how far it has got when it is handed a reading.
 */
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { OperationProgress } from "@shared/components/OperationProgress";
import { TRANSFER_STAGES, checklistStages, stageWords } from "@shared/types/operation";
import type { Operation, TransferProgress } from "@shared/types/operation";
import { verifyRows } from "@shared/encryption/verifyRows";

const READING: TransferProgress = {
  observedAt: "2026-11-09T09:15:00Z",
  sequence: 3,
  stage: "encrypting",
  backupSetId: "alpha/nightly",
  backupSetsDone: 0,
  backupSetsTotal: 1,
  artifact: "nightly.dump",
  artifactsDone: 0,
  bytesDone: 1024,
  bytesTotal: 4096
};

function operation(progress: TransferProgress): Operation {
  return {
    id: "op_1", setId: "alpha/nightly", setName: "Production PostgreSQL", kind: "transfer",
    label: "run cycle", status: "running", progress, cycle: null, nonDestructive: false,
    startedAt: "2026-11-09T09:00:00Z"
  };
}

describe("encrypted copy progress stages (#1290)", () => {
  it("names both stages, after the original five and before complete", () => {
    expect(TRANSFER_STAGES).toEqual([
      "discovering", "transferring", "verifying", "committing", "cleaning-remote",
      "encrypting", "decrypt-verifying", "complete"
    ]);
  });

  it("keeps the two stages off the checklist of a plaintext transfer", () => {
    expect(checklistStages("transferring")).toEqual(["discovering", "transferring", "verifying", "committing", "cleaning-remote", "complete"]);
  });

  it("puts the two stages on the checklist when one of them is the current stage", () => {
    for (const stage of ["encrypting", "decrypt-verifying"] as const) {
      expect(checklistStages(stage)).toContain("encrypting");
      expect(checklistStages(stage)).toContain("decrypt-verifying");
    }
  });

  it("says the stage's name the way a person writes it", () => {
    expect(stageWords("encrypting")).toBe("encrypting");
    expect(stageWords("decrypt-verifying")).toBe("decrypt-verifying");
    expect(stageWords("cleaning-remote")).toBe("cleaning remote");
  });

  it("marks Encrypting as the one current step and shows its bytes", () => {
    render(<OperationProgress operation={operation(READING)} />);

    const current = screen.getAllByRole("listitem").filter((li) => li.getAttribute("aria-current") === "step");
    expect(current).toHaveLength(1);
    expect(current[0].textContent).toContain("Encrypting");
    expect(screen.getByText("1.0 KB / 4.0 KB")).toBeTruthy();
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("25");
  });

  it("marks Decrypt-verifying as current with Encrypting ticked before it", () => {
    render(<OperationProgress operation={operation({ ...READING, stage: "decrypt-verifying" })} />);

    const items = screen.getAllByRole("listitem");
    const current = items.filter((li) => li.getAttribute("aria-current") === "step");
    expect(current).toHaveLength(1);
    expect(current[0].textContent).toContain("Decrypt-verifying");
    expect(items.some((li) => li.textContent?.includes("Encrypting"))).toBe(true);
  });

  it("draws no Encrypting row for a plaintext transfer", () => {
    render(<OperationProgress operation={operation({ ...READING, stage: "transferring" })} />);

    expect(screen.queryByText("Encrypting")).toBeNull();
    expect(screen.queryByText("Decrypt-verifying")).toBeNull();
  });

  it("draws the bar indeterminate when an encrypted stage has no total", () => {
    const unsized: typeof READING = { ...READING };
    delete unsized.bytesTotal;
    render(<OperationProgress operation={operation(unsized)} />);

    expect(screen.getByRole("progressbar").hasAttribute("aria-valuenow")).toBe(false);
  });

  it("lets the decrypt row carry its byte reading, and only for that stage", () => {
    const withBytes = verifyRows(null, { stage: "decrypt-verifying", bytesDone: 2048, bytesTotal: 4096 });
    expect(withBytes.find((r) => r.id === "decrypt")?.label).toBe("Decrypt-verifying 2.0 KB / 4.0 KB");

    const wrongStage = verifyRows(null, { stage: "encrypting", bytesDone: 2048, bytesTotal: 4096 });
    expect(wrongStage.find((r) => r.id === "decrypt")?.label).toBe("Decrypt-verifying");
    expect(verifyRows(null).find((r) => r.id === "decrypt")?.label).toBe("Decrypt-verifying");
  });
});
