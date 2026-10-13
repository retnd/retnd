import type { Bucket } from "./claims";

/** How a filtered list says what it was narrowed to, and how a count's link is
 *  named. One table, so the Dashboard's link, the list's status line and the
 *  disabled reason cannot word the same bucket three ways. */
const PHRASE: Record<Bucket, string> = {
  due: "that are due",
  overdue: "that are overdue",
  degraded: "that are degraded",
  untested: "with no recovery test"
};

export const bucketPhrase = (b: Bucket): string => PHRASE[b];

/** "Show 1 due backup", "Show 0 overdue backups", "Show 1 backup with no recovery test". */
export function bucketLinkName(b: Bucket, n: number): string {
  const noun = n === 1 ? "backup" : "backups";
  return b === "untested" ? "Show " + n + " " + noun + " with no recovery test" : "Show " + n + " " + b + " " + noun;
}

/** Why a zero count cannot be followed. */
export const BUCKET_NONE: Record<Bucket, string> = {
  due: "Nothing is due.",
  overdue: "Nothing is overdue.",
  degraded: "Nothing is degraded.",
  untested: "Every backup has a recovery test on record."
};
