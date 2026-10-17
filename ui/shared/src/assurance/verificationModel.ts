/**
 * What is known about the verification of every backup, as ONE model on the
 * app's causl graph (issue #1134).
 *
 * # Why this exists
 *
 * Content verified at T, structure readable at T2, which check stood behind
 * it, and whether the set follows the deployment's default policy were each
 * worked out separately by the Backups list, the backup's lifecycle, its
 * Copies block, its Assurance card, the set's Validation block, the Backup
 * Sets cards and the Dashboard tally. Each read a different slice, so they
 * disagreed: a backup was "Content verified" in one place and "nothing has
 * validated this backup" in the next, and the Recovery chip said Not
 * configured beside a recovery test that had passed.
 *
 * # How it is built
 *
 * Four inputs and three derived nodes, and every surface reads the derived
 * ones (`graph.dependencies()` shows the same shape):
 *
 *   claimsNode      every backup's composed claims        (input)
 *   policiesNode    each set's resolved policy            (input)
 *   setTestsNode    each set's newest recorded test       (input)
 *   revisionNode    bumped when a scrub or test ends      (input)
 *
 *   factsNode       one BackupFacts per backup            (derived: the three inputs)
 *   setFactsNode    one SetFacts per set                  (derived: factsNode)
 *   tallyNode       Due, Overdue, Degraded, No test       (derived: factsNode)
 *
 * # What the subscription does
 *
 * `noteAssuranceChanged()` commits the revision input. A subscriber on that
 * node starts a refetch of the three data inputs and commits them in ONE
 * transaction, so every mounted surface re-renders from the same fact set
 * together. The subscriber itself never commits: nested commits throw
 * `CommitInProgressError`, so it starts the async read and the commit lands
 * after the read returns. A derived node is a pure synchronous function, so
 * the fetching cannot live in the graph; it is the one thing outside it.
 *
 * # What the set's own test is, and what it is not
 *
 * A recovery test is recorded against one backup. The set's newest test is a
 * fact about the SET, shown beside a backup that has no record of its own
 * (`recoveryView.elsewhere`). It is never turned into that backup's own
 * claim: the chips exist to say what was and was not tested.
 */
import { useEffect } from "react";
import { useOptionalApi } from "@shared/api/ApiContext";
import type { BackupSetAssurance, RetndApi } from "@shared/api/contracts";
import { graph, registerInput, useCausl } from "@shared/state/graph";
import { BUCKETS, DEFAULTS_ANCHOR, DEFAULTS_LINK, ageOf, bucketsOf, neverConfiguredWording, renderClaim, renderStored } from "./claims";
import type { Bucket, PolicyBasis, RenderState, RenderedClaim } from "./claims";
import type { ArtifactAssurance, ValidatorResult } from "./types";

export { DEFAULTS_ANCHOR, DEFAULTS_LINK };

/** Where a set's resolved policy comes from, and what it says about recovery tests. */
export interface SetPolicyFact {
  setId: string;
  source: "default" | "own";
  mode: string;
  effectiveCheck?: "builtin" | "validator" | "none";
  /** The engine's dry run of "is a validator named"; absent on an older server. */
  validatorDeclared?: boolean;
}

/** What a control says beside a recovery test the set can only refuse
 *  (#1108): the Dashboard's rows and the Backups row menu say it alike. */
export const NO_VALIDATOR_REASON = "No recovery-test validator is declared for this set, so a test can only be refused.";

/** Whether a recovery test on this set could only be refused, so a button that
 *  starts one is not offered (#1108, #1111). The engine's own answer
 *  (effective_check) wins; a server that does not send it is read from
 *  validator_declared. Unknown is not a refusal. */
export function recoveryTestWouldBeRefused(policy: SetPolicyFact | null | undefined): boolean {
  if (!policy) return false;
  if (policy.effectiveCheck === "builtin" || policy.effectiveCheck === "validator") return false;
  if (policy.effectiveCheck === "none") return true;
  return policy.validatorDeclared === false;
}

/** The newest recovery test recorded for a set, and the backup it ran on. */
export interface SetTestFact {
  setId: string;
  artifactId: string;
  medium: string;
  state: ValidatorResult["state"];
  at: string;
}

/** Where the model reads from. The production source is `sourceFromApi`. */
export interface AssuranceSource {
  claims(): Promise<ArtifactAssurance[]>;
  /** The set's resolved policy; null when it resolves to nothing. A read that
   *  fails rejects, which the model keeps apart from null (issue #1141). */
  policy(setId: string): Promise<SetPolicyFact | null>;
  /** The set's newest recorded test; null when it is KNOWN that none was
   *  recorded, undefined when the server cannot say. */
  setTest(setId: string): Promise<SetTestFact | null | undefined>;
}

/** What the Recovery chip draws for one backup (or one set). Ages are not
 *  here: they are worked out at render, from the records, so a page left open
 *  does not keep saying "5h ago" for a day. */
export interface RecoveryView {
  state: RenderState;
  label: string;
  /** What is known about the configuration behind the label. */
  basis: PolicyBasis;
  /** In-app path the label links to. */
  link?: string;
  /** "No recovery test has run yet", said only when it is known that none has. */
  untested?: string;
  /** What the policy does, in words, beside a Recovery claim that has no result. */
  effective?: string;
  /** The set's newest test when it is on another backup (or, for a set, at all). */
  elsewhere?: { artifactId: string; filename: string; state: SetTestFact["state"]; at: string };
}

export interface BackupFacts {
  artifactId: string;
  setId: string;
  /** The composed claims, exactly as the server side of the seam gave them. */
  assurance: ArtifactAssurance;
  stored: ReturnType<typeof renderStored>;
  content: RenderedClaim;
  /** When the strongest copy was content verified, if any copy was. */
  contentAt?: string;
  /** This backup's OWN recovery claim. */
  recovery: RenderedClaim;
  recoveryView: RecoveryView;
  /** The structure check or validator pass on this backup, if one passed. */
  structure: { at: string; check: "builtin" | "validator" } | null;
  /** null: the set's policy resolves to nothing. undefined: it could not be read. */
  policy: SetPolicyFact | null | undefined;
  /** null: no test is on record. undefined: the server cannot say. */
  setTest: SetTestFact | null | undefined;
}

export interface FactsIndex {
  ready: boolean;
  error: boolean;
  byArtifact: Record<string, BackupFacts>;
}

export interface LastValidation {
  kind: "recovery" | "content" | "none";
  state?: SetTestFact["state"];
  at?: string;
  text: string;
}

export interface SetFacts {
  setId: string;
  backups: number;
  policy: SetPolicyFact | null | undefined;
  setTest: SetTestFact | null | undefined;
  /** The backup with the newest content verification, and when. */
  newestContent: { artifactId: string; at: string } | null;
  lastValidation: LastValidation;
  recoveryView: RecoveryView;
}

export interface SetFactsIndex {
  ready: boolean;
  bySet: Record<string, SetFacts>;
}

export interface Tally {
  /** False until the claims have been read, and after a read that failed. A
   *  zero in an unknown tally is not a zero. */
  known: boolean;
  total: number;
  due: number;
  overdue: number;
  degraded: number;
  untested: number;
  /** The backups each count is made of; the Backups list filters by these. */
  ids: Record<Bucket, string[]>;
}

// ---------------------------------------------------------------- inputs

interface ClaimsInput { status: "loading" | "ready" | "error"; data: ArtifactAssurance[] }

export const claimsNode = registerInput<ClaimsInput>("app.assurance.claims", { status: "loading", data: [] });
/** A set absent from policiesNode is one whose policy could not be read; null is a set whose policy resolves to nothing. */
export const policiesNode = registerInput<Record<string, SetPolicyFact | null>>("app.assurance.policies", {});
/** A set absent from setTestsNode is one whose newest test the server cannot say; null is "none recorded". */
export const setTestsNode = registerInput<Record<string, SetTestFact | null>>("app.assurance.setTests", {});
export const revisionNode = registerInput<number>("app.assurance.revision", 0);

// ------------------------------------------------------------ pure parts

const setOf = (artifactId: string): string => artifactId.split("/").slice(0, 2).join("/");
const fileOf = (artifactId: string): string => artifactId.split("/").pop() ?? artifactId;

const TEST_WORDS: Record<SetTestFact["state"], string> = {
  passed: "Recovery test passed",
  readable: "Readable (built-in check)",
  failed: "Recovery test failed",
  refused_for_cost: "Recovery test skipped (cost)",
  could_not_complete: "Recovery test could not complete"
};

/** The effective state of a policy, in words, for a claim with no result. */
function effectiveWords(policy: SetPolicyFact | null | undefined): string | undefined {
  if (!policy) return undefined;
  if (policy.mode !== "off") return "Recovery tests are " + policy.mode;
  if (policy.effectiveCheck === "builtin") return "Recovery tests are off, a manual run uses the built-in check";
  if (policy.effectiveCheck === "none") return "Recovery tests are off, and no check is available for a manual run";
  return "Recovery tests are off";
}

/** The set's configuration as the one wording rule (claims.ts) reads it. */
function basisOf(policy: SetPolicyFact | null | undefined): PolicyBasis {
  return policy === undefined ? "unavailable" : policy === null ? "none" : policy.source;
}

/** What a never_configured claim says about the set's configuration. Only a
 *  set whose newest test is KNOWN to be absent may say that nothing has run. */
function wordingOf(policy: SetPolicyFact | null | undefined, setTest: SetTestFact | null | undefined, setId: string) {
  return { basis: basisOf(policy), setId, noRunYet: setTest === null };
}

const NO_RESULT: readonly RenderState[] = ["never_configured", "never_run", "unknown"];

function factsOf(a: ArtifactAssurance, policy: SetPolicyFact | null | undefined, setTest: SetTestFact | null | undefined, now: number): BackupFacts {
  const setId = setOf(a.artifactId);
  const w = wordingOf(policy, setTest, setId);
  const content = renderClaim("content", a.content, now, a.copyContent, a.copies, { ...w, noRunYet: false });
  const recovery = renderClaim("recovery", a.recovery, now, undefined, undefined, w);
  const contentAt = a.content.achievedClass === "content" && (a.content.status === "pass" || a.content.status === "overdue") ? a.content.at : undefined;
  const structure = recovery.state === "pass" || recovery.state === "pass_builtin"
    ? (a.recovery.at ? { at: a.recovery.at, check: a.recovery.check === "builtin" ? "builtin" as const : "validator" as const } : null)
    : null;
  let view: RecoveryView = { state: recovery.state, label: recovery.label, basis: w.basis };
  if (NO_RESULT.includes(recovery.state)) {
    const elsewhere = setTest && setTest.artifactId !== a.artifactId
      ? { artifactId: setTest.artifactId, filename: fileOf(setTest.artifactId), state: setTest.state, at: setTest.at }
      : undefined;
    if (recovery.state === "never_configured") {
      view = {
        state: recovery.state, label: recovery.label, basis: w.basis, link: recovery.link, untested: recovery.note, effective: effectiveWords(policy)
      };
    }
    if (elsewhere) view = { ...view, elsewhere };
  }
  return { artifactId: a.artifactId, setId, assurance: a, stored: renderStored(a.copies), content, contentAt, recovery, recoveryView: view, structure, policy, setTest };
}

/** "Readable (built-in check) 5h ago" and its siblings, worked out at the
 *  moment it is drawn so the age is the age now. */
export function describeLastValidation(v: Pick<LastValidation, "kind" | "state" | "at">, now: number = Date.now()): string {
  const age = ageOf(v.at, now);
  if (v.kind === "recovery" && v.state) return TEST_WORDS[v.state] + (age ? " " + age : "");
  if (v.kind === "content") return "Content verified" + (age ? " " + age : "") + ", no recovery test";
  return "None recorded";
}

export function testWords(state: SetTestFact["state"]): string {
  return state === "readable" ? "readable (built-in check)" : state === "passed" ? "passed" : state.replace(/_/g, " ");
}

function setFactsOf(setId: string, list: BackupFacts[], policy: SetPolicyFact | null | undefined, setTest: SetTestFact | null | undefined, now: number): SetFacts {
  let newestContent: SetFacts["newestContent"] = null;
  for (const f of list) {
    if (f.contentAt && (!newestContent || Date.parse(f.contentAt) > Date.parse(newestContent.at))) newestContent = { artifactId: f.artifactId, at: f.contentAt };
  }
  const lv: Omit<LastValidation, "text"> = setTest
    ? { kind: "recovery", state: setTest.state, at: setTest.at }
    : newestContent ? { kind: "content", at: newestContent.at } : { kind: "none" };
  const w = wordingOf(policy, setTest, setId);
  const named = neverConfiguredWording("recovery", w.basis, w);
  const recoveryView: RecoveryView = {
    state: "never_configured", label: named.label, basis: w.basis, link: named.link, untested: named.note, effective: effectiveWords(policy),
    ...(setTest ? { elsewhere: { artifactId: setTest.artifactId, filename: fileOf(setTest.artifactId), state: setTest.state, at: setTest.at } } : {})
  };
  return { setId, backups: list.length, policy, setTest, newestContent, lastValidation: { ...lv, text: describeLastValidation(lv, now) }, recoveryView };
}

// --------------------------------------------------------------- derived

export const factsNode = graph.derived<FactsIndex>("app.assurance.facts", (get) => {
  const claims = get(claimsNode);
  const policies = get(policiesNode);
  const tests = get(setTestsNode);
  const now = Date.now();
  const byArtifact: Record<string, BackupFacts> = {};
  for (const a of claims.data) {
    const setId = setOf(a.artifactId);
    byArtifact[a.artifactId] = factsOf(a, policies[setId], tests[setId], now);
  }
  return { ready: claims.status === "ready", error: claims.status === "error", byArtifact };
});

export const setFactsNode = graph.derived<SetFactsIndex>("app.assurance.setFacts", (get) => {
  const facts = get(factsNode);
  const bySet: Record<string, BackupFacts[]> = {};
  for (const f of Object.values(facts.byArtifact)) (bySet[f.setId] ??= []).push(f);
  const now = Date.now();
  const out: Record<string, SetFacts> = {};
  for (const [setId, list] of Object.entries(bySet)) out[setId] = setFactsOf(setId, list, list[0].policy, list[0].setTest, now);
  return { ready: facts.ready, bySet: out };
});

export const tallyNode = graph.derived<Tally>("app.assurance.tally", (get) => {
  const facts = get(factsNode);
  const ids: Record<Bucket, string[]> = { due: [], overdue: [], degraded: [], untested: [] };
  const all = Object.values(facts.byArtifact);
  const now = Date.now();
  for (const f of all) for (const b of bucketsOf(f.assurance, now)) ids[b].push(f.artifactId);
  return {
    known: facts.ready, total: all.length,
    due: ids.due.length, overdue: ids.overdue.length, degraded: ids.degraded.length, untested: ids.untested.length, ids
  };
});

// ------------------------------------------------- the subscription half

let source: AssuranceSource | null = null;
let boundTo: unknown = null;
let pending: unknown = null;
let loadedAt = 0;
/** How old the model may be when a surface mounts before it reads again. */
const STALE_AFTER_MS = 30_000;
let seq = 0;
let inflight: Promise<void> = Promise.resolve();
let unsubscribe: (() => void) | null = null;
let lastSeenRevision = 0;

function listenForChanges(): void {
  if (unsubscribe) return;
  lastSeenRevision = graph.read(revisionNode);
  // Subscribing may report the current value straight away, which is not a
  // change: only a revision that moved starts a read.
  unsubscribe = graph.subscribe(revisionNode, () => {
    const now = graph.read(revisionNode);
    if (now === lastSeenRevision) return;
    lastSeenRevision = now;
    void refreshAssuranceModel();
  });
}

/** Points the model at a source. Reads nothing by itself. */
export function bindAssuranceSource(next: AssuranceSource): void {
  source = next;
  listenForChanges();
}

/** Reads the three data inputs and commits them together. A read that is
 *  overtaken by a later one is dropped, never allowed to land on top of it. */
export function refreshAssuranceModel(): Promise<void> {
  const src = source;
  if (!src) return Promise.resolve();
  const mine = ++seq;
  const run = (async () => {
    try {
      const claims = await src.claims();
      const sets = [...new Set(claims.map((c) => setOf(c.artifactId)))];
      const per = await Promise.all(sets.map(async (id) => ({
        id,
        // A read that fails is left out, not turned into "none": an absent
        // policy is drawn as unavailable and an absent test claims nothing.
        policy: await src.policy(id).catch(() => undefined),
        test: await src.setTest(id).catch(() => undefined)
      })));
      if (mine !== seq) return;
      loadedAt = Date.now();
      graph.commit("assurance/loaded", (tx) => {
        tx.set(claimsNode, { status: "ready", data: claims });
        tx.set(policiesNode, Object.fromEntries(per.filter((p) => p.policy !== undefined).map((p) => [p.id, p.policy as SetPolicyFact | null])));
        tx.set(setTestsNode, Object.fromEntries(per.filter((p) => p.test !== undefined).map((p) => [p.id, p.test as SetTestFact | null])));
      });
    } catch {
      if (mine !== seq) return;
      graph.commit("assurance/failed", (tx) => tx.set(claimsNode, { status: "error", data: [] }));
    }
  })();
  inflight = run;
  return run;
}

/** Resolves when the read that is in flight (if any) has landed. */
export function assuranceModelSettled(): Promise<void> {
  return inflight;
}

/** A scrub or recovery test ended, or a policy was saved: every surface that
 *  reads the model re-reads together. */
export function noteAssuranceChanged(): void {
  graph.commit("assurance/changed", (tx) => tx.set(revisionNode, graph.read(revisionNode) + 1));
}

/** Test-only: forgets the source, the subscription and any read in flight. */
export function resetAssuranceModelForTests(): void {
  source = null;
  boundTo = null;
  pending = null;
  loadedAt = 0;
  seq++;
  inflight = Promise.resolve();
  unsubscribe?.();
  unsubscribe = null;
}

// ----------------------------------------------------- the production side

/** The model's source over the app's API: the claims the assurance seam
 *  composes, each set's resolved policy, and the newest recorded test (only
 *  on a server that has the route to read it). */
export function sourceFromApi(api: RetndApi): AssuranceSource | null {
  const seam = api.assurance;
  if (!seam) return null;
  return {
    claims: () => seam.listArtifactAssurance(),
    policy: async (setId) => {
      const [s, n] = [setId.split("/")[0], setId.split("/").slice(1).join("/")];
      const p: BackupSetAssurance = await api.getBackupSetAssurance(s, n);
      return { setId, source: p.isOverride ? "own" : "default", mode: p.recoveryTest.mode, effectiveCheck: p.recoveryTest.effectiveCheck, validatorDeclared: p.recoveryTest.validatorDeclared };
    },
    setTest: async (setId) => {
      if (!seam.offers.operations) return undefined;
      const r = await seam.getValidatorResult(setId);
      return r ? { setId, artifactId: r.artifactId, medium: r.medium, state: r.state, at: r.recordedAt } : null;
    }
  };
}

/** Binds the model to the surrounding API and reads it once. Every surface
 *  that reads the model calls this, and the first one of an API does the
 *  work; the rest find it already done. */
export function useAssuranceModel(): void {
  const api = useOptionalApi();
  useEffect(() => {
    if (!api) return;
    // Already bound to this API and either read or being read: nothing to do.
    if (boundTo === api && (pending === api || (graph.read(claimsNode).status !== "loading" && Date.now() - loadedAt < STALE_AFTER_MS))) return;
    boundTo = api;
    const src = sourceFromApi(api);
    if (!src) {
      // A server that does not offer assurance: nothing is claimed.
      source = null;
      return;
    }
    bindAssuranceSource(src);
    pending = api;
    void refreshAssuranceModel().finally(() => { if (pending === api) pending = null; });
  }, [api]);
}

/** The facts of one backup, or null while they are not known. */
export function useBackupFacts(artifactId: string): BackupFacts | null {
  useAssuranceModel();
  return useCausl(factsNode).byArtifact[artifactId] ?? null;
}

export function useFactsIndex(): FactsIndex {
  useAssuranceModel();
  return useCausl(factsNode);
}

export function useSetFacts(setId: string): SetFacts | null {
  useAssuranceModel();
  return useCausl(setFactsNode).bySet[setId] ?? null;
}

export function useTally(): Tally {
  useAssuranceModel();
  return useCausl(tallyNode);
}

export { BUCKETS };
