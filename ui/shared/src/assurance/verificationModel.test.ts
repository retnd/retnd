/**
 * Issue #1134: the verification facts of one backup are ONE derived model on
 * the app's causl graph, and every surface reads them from it.
 *
 * These cases pin the model itself: what a backup's Recovery claim says when
 * its set follows the default, what the set's own newest test adds, that the
 * tally and the lists it links to are one definition, that the dependency
 * relationships are the ones the graph reports, and that a finished scrub or
 * recovery test moves every derived value in a single commit.
 */
import { afterEach, describe, expect, it } from "vitest";
import { graph, resetGraphForTests } from "@shared/state/graph";
import {
  bindAssuranceSource, claimsNode, factsNode, noteAssuranceChanged, policiesNode, refreshAssuranceModel,
  resetAssuranceModelForTests, setFactsNode, setTestsNode, tallyNode
} from "./verificationModel";
import type { AssuranceSource, SetPolicyFact, SetTestFact } from "./verificationModel";
import type { ArtifactAssurance, ClaimRecord } from "./types";

const HOUR = 3_600_000;
const SET = "production/postgres-primary";
const NEWEST = SET + "/postgres-prod-20260829.dump.zst";
const TESTED = SET + "/postgres-prod-20260828.dump.zst";
const ago = (h: number) => new Date(Date.now() - h * HOUR).toISOString();
const rec = (status: ClaimRecord["status"], cls: ClaimRecord["achievedClass"], h?: number, check?: ClaimRecord["check"]): ClaimRecord =>
  ({ status, achievedClass: cls, ...(h === undefined ? {} : { at: ago(h) }), ...(check ? { check } : {}) });

const claim = (artifactId: string, content: ClaimRecord, recovery: ClaimRecord): ArtifactAssurance =>
  ({ artifactId, copies: 1, copyContent: ["content"], content, recovery });

const newest = claim(NEWEST, rec("pass", "content", 10), rec("never_configured", ""));
const tested = claim(TESTED, rec("pass", "content", 26), rec("pass", "content", 5, "builtin"));

const followsDefault: SetPolicyFact = { setId: SET, source: "default", mode: "off", effectiveCheck: "builtin" };
const readableOnTested: SetTestFact = { setId: SET, artifactId: TESTED, medium: "local", state: "readable", at: ago(5) };

function source(over: Partial<{ claims: ArtifactAssurance[]; policy: SetPolicyFact | null; test: SetTestFact | null }> = {}) {
  const calls = { claims: 0, policy: 0, test: 0 };
  const state = { claims: [newest, tested], policy: followsDefault as SetPolicyFact | null, test: readableOnTested as SetTestFact | null, ...over };
  const src: AssuranceSource = {
    claims: async () => { calls.claims++; return state.claims; },
    policy: async () => { calls.policy++; return state.policy; },
    setTest: async () => { calls.test++; return state.test; }
  };
  return { src, calls, state };
}

afterEach(() => {
  resetAssuranceModelForTests();
  resetGraphForTests();
});

describe("#1134 the facts of one backup", () => {
  it("#1134 a backup with no recovery record of its own, in a set that follows the default, says so and links to the default", async () => {
    const { src } = source();
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const v = graph.read(factsNode).byArtifact[NEWEST].recoveryView;
    expect(v.label).toBe("Default configuration");
    expect(v.link).toBe("/settings#assurance-defaults");
    expect(v.state).toBe("never_configured");
    expect(v.effective).toBe("Recovery tests are off, a manual run uses the built-in check");
  });

  it("#1134 the label is Not configured only when the policy could not be read", async () => {
    const { src } = source({ policy: null });
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const v = graph.read(factsNode).byArtifact[NEWEST].recoveryView;
    expect(v.label).toBe("Not configured");
    expect(v.link).toBeUndefined();
  });

  it("#1134 a set with its own policy links to that set's card instead", async () => {
    const { src } = source({ policy: { setId: SET, source: "own", mode: "off" } });
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const v = graph.read(factsNode).byArtifact[NEWEST].recoveryView;
    expect(v.label).toBe("This set's own policy");
    expect(v.link).toBe("/sets/production/postgres-primary#assurance");
  });

  it("#1134 the set's newest test on another backup is a set fact beside the backup's own state, never the backup's", async () => {
    const { src } = source();
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const facts = graph.read(factsNode).byArtifact;
    expect(facts[NEWEST].recoveryView.elsewhere).toEqual({
      artifactId: TESTED, filename: "postgres-prod-20260828.dump.zst", state: "readable", at: readableOnTested.at
    });
    expect(facts[NEWEST].recovery.state).toBe("never_configured");
    expect(facts[NEWEST].structure).toBeNull();
    // the tested backup carries its own result and no pointer to itself
    expect(facts[TESTED].structure).toEqual({ at: tested.recovery.at, check: "builtin" });
    expect(facts[TESTED].recoveryView.elsewhere).toBeUndefined();
    expect(facts[TESTED].recovery.state).toBe("pass_builtin");
  });

  it("#1134 content verified at T is one value that the lifecycle, the copies and the chip all read", async () => {
    const { src } = source();
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const f = graph.read(factsNode).byArtifact[NEWEST];
    expect(f.contentAt).toBe(newest.content.at);
    expect(f.content.state).toBe("pass");
  });
});

describe("#1134 the sets and the tally", () => {
  it("#1134 a set's facts say what was last validated and how many backups the model knows", async () => {
    const { src } = source();
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const s = graph.read(setFactsNode).bySet[SET];
    expect(s.backups).toBe(2);
    expect(s.lastValidation.text).toMatch(/^Readable \(built-in check\) 5h ago$/);
    expect(s.recoveryView.label).toBe("Default configuration");
  });

  it("#1134 with a content pass and no test, the last validation says that and not nothing", async () => {
    const { src } = source({ claims: [newest], test: null });
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    expect(graph.read(setFactsNode).bySet[SET].lastValidation.text).toBe("Content verified 10h ago, no recovery test");
  });

  it("#1134 a set with nothing on record says None recorded", async () => {
    const { src } = source({ claims: [claim(NEWEST, rec("never_run", ""), rec("never_configured", ""))], test: null });
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    expect(graph.read(setFactsNode).bySet[SET].lastValidation.text).toBe("None recorded");
  });

  it("#1134 the tally counts exactly the backups the filters list, and counts the ones with no recovery test", async () => {
    const { src } = source();
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    const t = graph.read(tallyNode);
    expect(t).toMatchObject({ total: 2, due: 0, overdue: 0, degraded: 0, untested: 1 });
    expect(t.ids.untested).toEqual([NEWEST]);
    for (const k of ["due", "overdue", "degraded", "untested"] as const) expect(t.ids[k].length).toBe(t[k]);
  });

  it("#1134 an overdue recovery test is due and overdue and not untested", async () => {
    const overdue = claim(TESTED, rec("pass", "content", 5), rec("overdue", "", 700, "builtin"));
    const { src } = source({ claims: [overdue] });
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    expect(graph.read(tallyNode)).toMatchObject({ due: 1, overdue: 1, untested: 0 });
  });
});

describe("#1134 the dependency relationships are the graph's own", () => {
  it("#1134 facts depend on the three inputs, and the tally and the set facts depend on the facts", async () => {
    const deps = graph.dependencies(factsNode);
    expect(deps).toEqual(expect.arrayContaining([claimsNode.id, policiesNode.id, setTestsNode.id]));
    expect(graph.dependencies(tallyNode)).toContain(factsNode.id);
    expect(graph.dependencies(setFactsNode)).toContain(factsNode.id);
    expect(graph.dependents(factsNode)).toEqual(expect.arrayContaining([tallyNode.id, setFactsNode.id]));
    expect(graph.dependents(claimsNode)).toContain(factsNode.id);
  });
});

describe("#1134 a subscription moves every surface together", () => {
  it("#1134 noteAssuranceChanged refetches and lands the new facts, the tally and the set facts in ONE commit", async () => {
    const { src, calls, state } = source();
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    expect(graph.read(tallyNode).untested).toBe(1);
    const before = calls.claims;

    // The scrub or recovery test finishes: the newest backup now has a result.
    state.claims = [claim(NEWEST, rec("pass", "content", 0), rec("pass", "content", 0, "builtin")), tested];
    state.test = { setId: SET, artifactId: NEWEST, medium: "local", state: "readable", at: ago(0) };
    const times: number[] = [];
    graph.subscribe(factsNode, () => times.push(graph.now));
    graph.subscribe(tallyNode, () => times.push(graph.now));
    graph.subscribe(setFactsNode, () => times.push(graph.now));

    noteAssuranceChanged();
    await waitFor(() => graph.read(tallyNode).untested === 0);

    expect(calls.claims).toBe(before + 1);
    expect(graph.read(factsNode).byArtifact[NEWEST].recovery.state).toBe("pass_builtin");
    expect(graph.read(setFactsNode).bySet[SET].lastValidation.text).toBe("Readable (built-in check) just now");
    // all three derived values were notified by the same commit
    expect(new Set(times).size).toBe(1);
    expect(times.length).toBe(3);
  });

  it("#1134 an older refetch that lands late does not overwrite a newer one", async () => {
    const slow = source();
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => { release = r; });
    slow.src.claims = async () => { await gate; return [newest, tested]; };
    bindAssuranceSource(slow.src);
    const first = refreshAssuranceModel();
    const fresh = source({ claims: [claim(NEWEST, rec("pass", "content", 0), rec("pass", "content", 0, "builtin")), tested] });
    bindAssuranceSource(fresh.src);
    await refreshAssuranceModel();
    release();
    await first;
    expect(graph.read(factsNode).byArtifact[NEWEST].recovery.state).toBe("pass_builtin");
  });

  it("#1134 a source that cannot be read is an error state, never zeros", async () => {
    const { src } = source();
    src.claims = async () => { throw new Error("down"); };
    bindAssuranceSource(src);
    await refreshAssuranceModel();
    expect(graph.read(tallyNode).known).toBe(false);
    expect(graph.read(factsNode).error).toBe(true);
  });
});

async function waitFor(done: () => boolean) {
  for (let i = 0; i < 100 && !done(); i++) await new Promise((r) => setTimeout(r, 10));
  expect(done()).toBe(true);
}
