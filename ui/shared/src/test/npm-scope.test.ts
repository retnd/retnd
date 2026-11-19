/**
 * The two private workspace package names, asserted because npm validates
 * their dependency graphs without requiring package.json and package-lock.json
 * to agree on the root package name.
 */
import { describe, expect, it } from "vitest";

import uiSharedPackage from "../../package.json?raw";
import uiSharedLock from "../../package-lock.json?raw";
import conformancePackage from "../../../../apps/common/tests/package.json?raw";
import conformanceLock from "../../../../apps/common/tests/package-lock.json?raw";

/** The `name` of a parsed JSON document, narrowed rather than asserted:
 *  a document that is not an object, or carries no `name`, reads as
 *  `undefined` and fails the assertion that wanted one. */
function nameOf(node: unknown): unknown {
  if (node === null || typeof node !== "object" || !("name" in node)) return undefined;
  return node.name;
}

/** A lockfile's `packages[""]`, which is the root package's own entry and
 *  the second place the name is written down. */
function rootEntry(lock: unknown): unknown {
  if (lock === null || typeof lock !== "object" || !("packages" in lock)) return undefined;
  const packages = lock.packages;
  if (packages === null || typeof packages !== "object" || !("" in packages)) return undefined;
  return packages[""];
}

const WORKSPACES = [
  {
    path: "ui/shared",
    name: "@retnd/ui-shared",
    manifest: uiSharedPackage,
    lock: uiSharedLock
  },
  {
    path: "apps/common/tests",
    name: "@retnd/provider-conformance",
    manifest: conformancePackage,
    lock: conformanceLock
  }
] as const;

describe("the npm scopes", () => {
  for (const workspace of WORKSPACES) {
    describe(workspace.path, () => {
      it("publishes as " + workspace.name, () => {
        expect(nameOf(JSON.parse(workspace.manifest))).toBe(workspace.name);
      });

      /** A lockfile records the root name twice, at the top level and
       *  under `packages[""]`, and the two drift independently. */
      it("has a lockfile recording the same name in both places", () => {
        const lock: unknown = JSON.parse(workspace.lock);
        expect(nameOf(lock)).toBe(workspace.name);
        expect(nameOf(rootEntry(lock))).toBe(workspace.name);
      });

    });
  }
});
