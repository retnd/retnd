/**
 * Issue #1174: the Dashboard's Assurance tile and `retnd assurance list` both
 * classify every backup, one here (realAssurance's claims, claims.ts's
 * renderClaim and bucketsOf) and one in core/service/assurancelist.go. Until
 * the engine owns that classification, these vectors are what holds them
 * together: `go test ./service` reads the same files, so a change to either
 * side that the other doesn't follow turns one of them red.
 */
import { describe, expect, it } from "vitest";
import { bucketsOf, listingReason, renderClaim } from "@shared/assurance/claims";
import type { Bucket, PolicyBasis } from "@shared/assurance/claims";
import { contentClaim, copyContentOf, recoveryClaim } from "@shared/assurance/realAssurance";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";

interface VectorPlacement {
  medium?: string;
  status: string;
  access: string;
  scrub_state?: string;
  scrub_last_checked_at?: string;
  scrub_last_failure_at?: string;
  verification_class?: string;
  verified_at?: string;
  recovery_state?: string;
  recovery_tested_at?: string;
  recovery_check?: string;
}

interface VectorCase {
  name: string;
  now: string;
  artifact: { id: string; quarantined: boolean; placements: VectorPlacement[] };
  due?: { overdue: boolean; never: boolean };
  policy?: { off: boolean; basis: string };
  want: { content: string; recovery: string; buckets: string[]; reason: string };
}

const files = import.meta.glob("../../../../core/service/testdata/assurance-listing/*.json", {
  eager: true,
  import: "default"
}) as Record<string, { cases: VectorCase[] }>;

/** The Go side's bucket order, most urgent first. */
const ORDER: Bucket[] = ["degraded", "overdue", "due", "untested"];

function artifactOf(c: VectorCase): BackupArtifact {
  const placements = c.artifact.placements.map((p) => ({
    medium: p.medium ?? "local",
    status: p.status,
    access: p.access,
    assuranceState: p.scrub_state ?? null,
    lastCheckedAt: p.scrub_last_checked_at ?? null,
    lastFailureAt: p.scrub_last_failure_at ?? null,
    verificationClass: p.verification_class ?? null,
    verifiedAt: p.verified_at ?? null,
    recoveryState: p.recovery_state ?? null,
    recoveryTestedAt: p.recovery_tested_at ?? null,
    recoveryCheck: p.recovery_check ?? null
  })) as unknown as BackupPlacement[];
  const setId = c.artifact.id.split("/").slice(0, 2).join("/");
  return {
    id: c.artifact.id,
    setId,
    quarantine: c.artifact.quarantined ? {} : null,
    placements
  } as unknown as BackupArtifact;
}

const cases = Object.entries(files).flatMap(([file, doc]) =>
  doc.cases.map((c) => [file.split("/").pop() + " / " + c.name, c] as const)
);

describe("#1174 assurance listing vectors, shared with core/service", () => {
  it("reads the vector files", () => {
    expect(cases.length).toBeGreaterThanOrEqual(30);
  });

  it.each(cases)("%s", (_name, c) => {
    const now = Date.parse(c.now);
    const a = artifactOf(c);
    // A set missing from the policy map reads as tests on, with no policy;
    // "unavailable" is a policy read that failed.
    const basis = (c.policy?.basis ?? "none") as PolicyBasis;
    const policyOff = basis === "unavailable" ? undefined : (c.policy?.off ?? false);
    const content = contentClaim(a, c.due);
    const recovery = recoveryClaim(a, policyOff, true);
    const copies = copyContentOf(a);
    const shownContent = renderClaim("content", content, now, copies, copies.length, { basis });
    const shownRecovery = renderClaim("recovery", recovery, now, undefined, undefined, { basis });
    expect(shownContent.state).toBe(c.want.content);
    expect(shownRecovery.state).toBe(c.want.recovery);
    const buckets = bucketsOf({ content, recovery }, now);
    expect(ORDER.filter((b) => buckets.includes(b))).toEqual(c.want.buckets);
    expect(listingReason(shownContent, shownRecovery)).toBe(c.want.reason);
  });
});
