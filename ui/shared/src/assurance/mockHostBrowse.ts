/**
 * The host's folder listing, in memory (issue #1179).
 *
 * A small tree under the staging root, with the refusals the real host makes:
 * a name that climbs out of the root (400), a name that is not a folder (404,
 * DIRECTORY_NOT_FOUND), and three scenarios: `host-browse-unavailable`, a host
 * that cannot list at all; `host-browse-vanished`, whose root lists a folder
 * that is gone by the time it is opened; and `host-browse-session-ended`, a
 * listing refused because the session ended. Every path is relative to the
 * root, as the real endpoint's are.
 */
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode, StagingDirectory } from "@shared/api/contracts";
import type { Scenario } from "@shared/api/mock";

const TREE: Record<string, string[]> = {
  "": ["daily", "pg-restores", "weekly"],
  daily: ["2026-09", "2026-10"],
  "daily/2026-09": [],
  "daily/2026-10": ["week-41"],
  "daily/2026-10/week-41": [],
  "pg-restores": [],
  weekly: ["offsite-copy"],
  "weekly/offsite-copy": []
};

const refuse = (code: ApiErrorCode, message: string, status: number) =>
  new RetndError({ code, message, status, correlationId: "cid_mock" + code.toLowerCase() });

export function createMockHostBrowse(scenario: Scenario, root: string): (path: string) => Promise<StagingDirectory> {
  return (path) =>
    new Promise((resolve, reject) => {
      setTimeout(() => {
        if (scenario === "host-browse-unavailable") {
          reject(refuse("RECOVERY_UNAVAILABLE", "this deployment has no folder to browse", 503));
          return;
        }
        if (scenario === "host-browse-session-ended") {
          reject(refuse("UNAUTHENTICATED", "authentication required", 401));
          return;
        }
        if (path.split("/").some((seg) => seg === ".." || seg === ".") || path.startsWith("/") || path.includes("\\") || path.includes("//")) {
          reject(refuse("INVALID_REQUEST", "that is not a folder name under the browsable root", 400));
          return;
        }
        // The vanished scenario's root still names a folder that has gone.
        const kids = scenario === "host-browse-vanished" && path === "" ? [...TREE[""], "archive-2025"].sort() : TREE[path];
        if (!kids) {
          reject(refuse("DIRECTORY_NOT_FOUND", "there is no such folder under the browsable root", 404));
          return;
        }
        const cut = path.lastIndexOf("/");
        resolve({
          root,
          path,
          parent: path === "" ? null : cut < 0 ? "" : path.slice(0, cut),
          entries: kids.map((name) => ({ name, path: path === "" ? name : path + "/" + name })),
          truncated: false
        });
      }, 120);
    });
}
