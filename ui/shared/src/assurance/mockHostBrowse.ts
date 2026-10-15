/**
 * The host's folder listing, in memory (issue #1179).
 *
 * A small tree under the staging root, with the refusals the real host makes:
 * a name that climbs out of the root, a name that is not a folder, and (in the
 * `host-browse-unavailable` scenario) a host that cannot list at all. Every
 * path is relative to the root, as the real endpoint's are.
 */
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode, HostDirectory } from "@shared/api/contracts";
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

const refuse = (code: string, message: string, status: number) =>
  new RetndError({ code: code as ApiErrorCode, message, status, correlationId: "cid_mock" + code.toLowerCase() });

export function createMockHostBrowse(scenario: Scenario, root: string): (path: string) => Promise<HostDirectory> {
  return (path) =>
    new Promise((resolve, reject) => {
      setTimeout(() => {
        if (scenario === "host-browse-unavailable") {
          reject(refuse("RECOVERY_UNAVAILABLE", "this deployment has no folder to browse", 503));
          return;
        }
        if (path.split("/").some((seg) => seg === ".." || seg === ".") || path.startsWith("/") || path.includes("\\") || path.includes("//")) {
          reject(refuse("INVALID_REQUEST", "that is not a folder name under the browsable root", 400));
          return;
        }
        const kids = TREE[path];
        if (!kids) {
          reject(refuse("INVALID_REQUEST", "there is no such folder under the browsable root", 400));
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
