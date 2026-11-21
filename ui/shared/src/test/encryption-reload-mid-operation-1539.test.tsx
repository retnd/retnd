/**
 * EPIC N, N2.4 (#1539): a reload in the middle of an encrypted copy.
 *
 * A reload throws away everything the page held: the React tree, the
 * terminal store's window and cursor, any id a dialog was following. What the
 * page shows after it has to come from the feed the service still holds, and
 * nothing the page does on load may start, cancel or change anything.
 *
 * Here the reload is: unmount the tree, empty the terminal store
 * (resetTerminalForTests, which is what a fresh page load gives it), and
 * mount the dashboard again against the same service, mid-operation. The
 * service is the mock API with its live feed reporting a decrypt-verify that
 * is part way through, and every call the page makes is recorded. A call
 * that is not a read is a second operation, or worse.
 */
import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { DashboardActivity } from "@shared/pages/DashboardActivity";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import type { BackupSet } from "@shared/types/backup";
import type { LiveActivity, SetActivity } from "@shared/types/activity";
import type { LiveTransferStage } from "@shared/types/operation";
import { resetTerminalForTests } from "@shared/state/terminalStore";

const SET: BackupSet = {
  connectionUnverified: false,
  id: "production/postgres-primary",
  source: "production",
  set: "postgres-primary",
  name: "Production PostgreSQL",
  host: "prod-db-01.internal",
  port: 22,
  username: "backup-agent",
  remoteFolder: "/backups/postgresql/",
  includePatterns: ["*.dump.zst"],
  completionMethod: "completion-marker",
  stableForSeconds: 0,
  destination: "/data/backups/production/postgres/",
  retentionIsOverride: false,
  pollIntervalSeconds: null,
  effectivePollIntervalSeconds: 900,
  validations: ["transfer", "checksum"],
  state: "healthy",
  stateNote: "Verified nightly dump.",
  enabled: true,
  readOnly: false,
  readOnlyRetainedCount: 0,
  newestKnownGoodAt: "2026-08-29T02:01:01+02:00",
  lastRunAt: "2026-08-29T02:01:01+02:00",
  lastValidation: "passed",
  expectedIntervalHours: 24,
  retainedCount: 32,
  retainedBytes: 421 * 1024 ** 3,
  trustedHostKeys: [{ algorithm: "ssh-ed25519", fingerprint: "SHA256:test-fingerprint" }],
  trustedHostKeyRecordedAt: "2026-08-02T10:14:00+02:00",
  sshKeyId: "key_a1b2c3",
  engine: "artifact",
  incremental: null
};

const ARTIFACT = "postgres-prod-20260828.dump.zst";

function midOperation(stage: LiveTransferStage): LiveActivity {
  const set: SetActivity = {
    setId: SET.id,
    active: true,
    stage,
    artifact: ARTIFACT,
    artifactsCompleted: 0,
    artifactsTotal: 1,
    progressBasis: "artifacts",
    bytesTransferred: 1024 * 1024,
    bytesTotal: 4 * 1024 * 1024,
    bytesPerSecond: null,
    failures: 0,
    outcome: null,
    startedAt: "2026-11-19T07:00:00Z",
    finishedAt: null,
    events: [],
    truncated: false,
    dropped: false,
    oldestSequence: 0,
    latestSequence: 0
  };
  return { observedAt: "2026-11-19T07:01:00Z", epoch: "one-process", pollAfterMs: 60_000, sets: [set], deployment: null };
}

/** The service, mid-operation, with every call the page makes written down. */
function service(stage: LiveTransferStage): { api: RetndApi; calls: string[] } {
  const calls: string[] = [];
  const base: RetndApi = { ...createMockApi(), getLiveActivity: async () => midOperation(stage) };
  const api = new Proxy(base, {
    get(target, key, receiver) {
      const value = Reflect.get(target, key, receiver);
      if (typeof value !== "function") return value;
      return (...args: unknown[]) => {
        calls.push(String(key));
        return (value as (...a: unknown[]) => unknown).apply(target, args);
      };
    }
  });
  return { api, calls };
}

function mount(api: RetndApi) {
  return render(
    <MemoryRouter>
      <ApiProvider api={api}>
        <DashboardActivity sets={[SET]} />
      </ApiProvider>
    </MemoryRouter>
  );
}

/** What the page says about the operation: the stage and its byte readout. */
async function showsTheStage(lead: RegExp) {
  await act(async () => {});
  expect((await screen.findAllByText(lead)).length).toBeGreaterThan(0);
  expect(screen.getAllByText(/1\.0 MB of 4\.0 MB/).length).toBeGreaterThan(0);
}

const notARead = (calls: string[]) => calls.filter((c) => !/^(get|list)/.test(c));

afterEach(() => {
  cleanup();
  resetTerminalForTests();
});

describe("a reload in the middle of an encrypted copy (N2.4)", () => {
  for (const [stage, lead] of [["decrypt-verifying", /Decrypt-verifying/], ["encrypting", /^Encrypting/]] as const) {
    it(`shows ${stage} again from the service's feed and starts nothing`, async () => {
      const { api, calls } = service(stage);

      mount(api);
      await showsTheStage(lead);
      const beforeReload = calls.length;
      expect(calls).toContain("getLiveActivity");

      // The reload: the tree and everything the page held are gone.
      cleanup();
      resetTerminalForTests();
      expect(screen.queryAllByText(lead)).toHaveLength(0);

      mount(api);
      await showsTheStage(lead);

      // The second page load read the feed itself rather than finding the
      // first one's window still in memory, which is what makes the stage
      // on screen a restored one.
      expect(calls.slice(beforeReload)).toContain("getLiveActivity");
      expect(notARead(calls)).toEqual([]);
    });
  }

  it("control: the recorder does see a call that is not a read", async () => {
    const { api, calls } = service("decrypt-verifying");
    await api.startDecryptVerify({ artifactId: SET.id + "/" + ARTIFACT, medium: "offsite", acknowledgeEgress: true }).catch(() => undefined);
    expect(notARead(calls)).toEqual(["startDecryptVerify"]);
  });
});
