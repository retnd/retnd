/**
 * Issue #1503: "Earlier lines are not held here any more. The full record is
 * on the Activity page." flashes under the download progress of a busy set.
 *
 * What I expect: once lines have really been lost for a run of a set, the
 * notice stays. It does not come and go with the next poll. Telling an
 * operator there is a gap and then taking it back a second later (when it is
 * still true) teaches them to ignore the real one.
 *
 * The "activity-busy-set" scenario gives the first set a service ring of 250
 * lines and a burst of forty new lines on every other poll, so the page's
 * 300-line window trims on one poll and reaches the service's oldest line on
 * the next. The specs watch the notice for as long as it takes to see both.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const NOTICE = "Earlier lines are not held here any more";
const firstStrip = (p: Page) => p.getByRole("region", { name: /^Activity for / }).first();

/** Samples the notice in the first strip every 50ms for `ms` and returns the
 *  visibility at each sample, so a spec can count how often it changed. */
async function watchNotice(p: Page, ms: number): Promise<boolean[]> {
  return p.evaluate(
    ({ text, ms: total }) =>
      new Promise<boolean[]>((resolve) => {
        const seen: boolean[] = [];
        const timer = window.setInterval(() => {
          // Looked up each time: a strip that remounts mid-watch must not
          // leave the sampler reading a detached node.
          const strip = document.querySelector('section[aria-label^="Activity for "]');
          seen.push(!!strip && (strip.textContent ?? "").includes(text));
        }, 50);
        window.setTimeout(() => {
          window.clearInterval(timer);
          resolve(seen);
        }, total);
      }),
    { text: NOTICE, ms }
  );
}

const flips = (seen: boolean[]) => seen.reduce((n, v, i) => (i > 0 && v !== seen[i - 1] ? n + 1 : n), 0);

test.describe("#1503 the dropped-lines notice does not flash on a busy set", () => {
  test("#1503 the set is really busy: its log keeps filling while the strip is watched", async ({ page }) => {
    await open(page, "/", "activity-busy-set");
    const strip = firstStrip(page);
    await expect(strip).toBeVisible();
    await expect(strip.getByText(/\d+ lines?/).first()).toBeVisible();
    const before = Number((await strip.getByText(/\d+ lines?/).first().textContent())!.match(/(\d+)/)![1]);
    await page.waitForTimeout(4000);
    const after = Number((await strip.getByText(/\d+ lines?/).first().textContent())!.match(/(\d+)/)![1]);
    expect(after).toBeGreaterThan(before);
  });

  test("#1503 once the notice has appeared it stays: it never comes back off", async ({ page }) => {
    await open(page, "/", "activity-busy-set");
    await expect(firstStrip(page)).toBeVisible();
    const seen = await watchNotice(page, 14_000);
    // Lines were lost on the first burst, so the notice must have been shown.
    expect(seen.some(Boolean)).toBe(true);
    // And once shown it is never taken back while the same run is on screen.
    const firstShown = seen.indexOf(true);
    expect(seen.slice(firstShown).every(Boolean)).toBe(true);
  });

  test("#1503 the notice changes visibility at most once over fourteen seconds of polling", async ({ page }) => {
    await open(page, "/", "activity-busy-set");
    await expect(firstStrip(page)).toBeVisible();
    const seen = await watchNotice(page, 14_000);
    expect(flips(seen)).toBeLessThanOrEqual(1);
  });
});
