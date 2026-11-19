/**
 * Issue #1141: a chip drawn from a record alone (the model has not answered for
 * that backup) never says "Not configured" for a claim nobody has configured.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ClaimChips } from "@shared/assurance/ClaimChips";
import type { ArtifactAssurance } from "@shared/assurance/types";

const a = {
  artifactId: "production/postgres-primary/x.zst", copies: 1, copyContent: ["content"],
  content: { status: "pass", achievedClass: "content", at: new Date(Date.now() - 3600_000).toISOString() },
  recovery: { status: "never_configured", achievedClass: "" }
} as ArtifactAssurance;

describe("#1141 ClaimChips from the record alone", () => {
  it("#1141 says Loading configuration while the policy has not arrived", () => {
    render(<ClaimChips assurance={a} />);
    expect(screen.getByText("Loading configuration")).toBeTruthy();
    expect(screen.queryByText("Not configured")).toBeNull();
  });

  it("#1141 says Configuration unavailable once the model has answered without this backup", () => {
    render(<ClaimChips assurance={a} configuration="unavailable" />);
    expect(screen.getByText("Configuration unavailable")).toBeTruthy();
    expect(screen.queryByText("Not configured")).toBeNull();
  });
});
