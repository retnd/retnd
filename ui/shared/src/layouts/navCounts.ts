/**
 * How the nav's count badges read, kept out of AppShell so that file exports
 * components only.
 */

/** The most the Backups badge spells out. Past it the badge reads ">100"
 *  (#1186): a long number crowds a 206px sidebar, and past a hundred the
 *  glance is "a lot", not the exact figure. */
export const BACKUPS_BADGE_CAP = 100;

/** How the Backups badge reads (#1186): the exact number up to the cap,
 *  ">100" past it, and a spoken form that says what it counts. The cap is a
 *  sidebar-width decision and lives only here; `retnd artifacts --count`
 *  and GET /system/health's retained_count always give the number. */
export function formatBackupsCount(n: number): { text: string; label: string } {
  const what = " outside quarantine";
  if (n > BACKUPS_BADGE_CAP) return { text: ">" + BACKUPS_BADGE_CAP, label: "more than " + BACKUPS_BADGE_CAP + " backups" + what };
  return { text: String(n), label: n + (n === 1 ? " backup" : " backups") + what };
}
