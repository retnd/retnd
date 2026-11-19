/**
 * Issue #1179, from the review of #1185: the logic under "Download in this
 * browser" for a selection, driven against a stub of the mint call. The
 * browser behaviour (links, counts, the ceiling on the bar) is
 * e2e/1179-bounded-download.spec.ts's job; this covers what a fixture cannot
 * reach cheaply: a run that is abandoned stops asking the host for grants,
 * and one refusal does not stop the rest.
 */
import { describe, expect, it } from "vitest";
import { RetndError } from "@shared/api/contracts";
import type { RecoveryMint, RetndApi } from "@shared/api/contracts";
import { DEFAULT_DOWNLOAD_STREAMS, downloadCeilingOf, mintDownloadLinks } from "@shared/assurance/bulkDownload";
import type { BackupArtifact } from "@shared/types/backup";

const file = (n: number): BackupArtifact =>
  ({
    id: "production/pg/f" + n + ".bak", setId: "production/pg", filename: "f" + n + ".bak", sizeBytes: 1,
    placements: [{ medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "", verificationClass: "content", verifiedAt: "", access: "immediate", status: "ACTIVE" }]
  }) as unknown as BackupArtifact;

const grant = (id: string, n: number): RecoveryMint => ({
  kind: "grant", artifactId: id, copy: "local", grant: "g" + n, downloadUrl: "/api/v1/recoveries/g" + n,
  expiresAt: "2026-10-15T00:02:00Z", target: { kind: "download" }
});

describe("#1179 minting the download links", () => {
  it("stops asking for grants once the run is abandoned", async () => {
    const ac = new AbortController();
    const asked: string[] = [];
    const api = {
      mintRecovery: async (id: string) => {
        asked.push(id);
        if (asked.length === 1) ac.abort();
        return grant(id, asked.length);
      }
    } as unknown as RetndApi;
    await mintDownloadLinks(api, [file(1), file(2), file(3)], ac.signal);
    expect(asked).toEqual(["production/pg/f1.bak"]);
  });

  it("lists a refusal with the host's reason and carries on", async () => {
    let n = 0;
    const api = {
      mintRecovery: async (id: string) => {
        n++;
        if (n === 1) throw new RetndError({ code: "RECOVERY_TOO_MANY_GRANTS", message: "Too many recoveries are waiting to be collected.", status: 429, correlationId: "c" });
        return grant(id, n);
      }
    } as unknown as RetndApi;
    const out = await mintDownloadLinks(api, [file(1), file(2)], new AbortController().signal);
    expect(out.links.map((l) => l.filename)).toEqual(["f2.bak"]);
    expect(out.links[0]).toMatchObject({ url: "/api/v1/recoveries/g2", expiresAt: "2026-10-15T00:02:00Z" });
    expect(out.skipped).toHaveLength(1);
    expect(out.skipped[0].filename).toBe("f1.bak");
    expect(out.skipped[0].reason).toMatch(/waiting to be collected/);
  });

  it("takes the engine's ceiling when it reports one, and the engine's default when it does not", () => {
    expect(DEFAULT_DOWNLOAD_STREAMS).toBe(4);
    expect(downloadCeilingOf({ maxDownloadStreams: 2 })).toBe(2);
    expect(downloadCeilingOf({ maxDownloadStreams: 0 })).toBe(4);
    expect(downloadCeilingOf(null)).toBe(4);
  });
});
