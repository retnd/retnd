/**
 * The development composition's API: the in-process mock, configured from the
 * page's query string (scenario picker, `wire=1`, `wire-restore=1`) and the
 * browser specs' live probe.
 *
 * This module is the only thing that statically imports @shared/api/mock, and
 * createApp reaches it only through a dynamic import() behind
 * `import.meta.env.DEV`. A production build folds that condition to false, so
 * the import() disappears and neither this file nor the several thousand
 * lines of fixtures behind it are emitted (#1216).
 */
import { createMockApi, liveProbeFromWindow, scenarioFromLocation, wireFromLocation, wireRestoreFromLocation } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";

export function createDevApi(): RetndApi {
  return createMockApi(scenarioFromLocation(), {
    wire: wireFromLocation(),
    wireRestore: wireRestoreFromLocation(),
    liveProbe: liveProbeFromWindow()
  });
}
