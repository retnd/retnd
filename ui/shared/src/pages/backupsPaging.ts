/**
 * How the Backups page pages its list (issue #1180).
 *
 * The list endpoint answers with every backup it holds, so paging is done over
 * what the page already has: nothing here asks the server for a window, and a
 * page boundary never hides a backup from select-all, which reads the whole
 * filtered list (#1107).
 *
 * Three rules, each with a reason:
 *
 *  - The size is 20, 50 or 100, or "all". "All" is offered only while the
 *    deployment holds fewer than {@link ALL_BELOW} backups, because rendering a
 *    row per backup past that is a page that stops responding.
 *  - 20, 50 and 100 are remembered between sessions in localStorage. "All" is
 *    not: it is a decision about this list at this size, and coming back to a
 *    page that has since grown to thousands of rows because last week it was a
 *    few hundred is not what anybody chose.
 *  - Storage is a convenience and nothing depends on it. A browser that blocks
 *    it, or holds a value this code does not know, gets the default and a page
 *    that works.
 */
export const PAGE_SIZES = [20, 50, 100] as const;
export type PageSize = (typeof PAGE_SIZES)[number];
export type PageChoice = PageSize | "all";
export const DEFAULT_PAGE_SIZE: PageSize = 20;
/** "All" is offered only while the deployment holds fewer backups than this. */
export const ALL_BELOW = 1000;
export const PAGE_SIZE_KEY = "retnd.backups.pageSize";

const isPageSize = (n: number): n is PageSize => (PAGE_SIZES as readonly number[]).includes(n);

/** The remembered size, or the default when there is none, it is not one of the
 *  three, or storage cannot be read at all. */
export function readPageSize(): PageSize {
  try {
    const raw = window.localStorage.getItem(PAGE_SIZE_KEY);
    const n = raw === null ? NaN : Number(raw);
    return isPageSize(n) ? n : DEFAULT_PAGE_SIZE;
  } catch {
    return DEFAULT_PAGE_SIZE;
  }
}

/** Remembers a size. Only 20, 50 and 100 are written; a failure is ignored. */
export function rememberPageSize(size: PageSize): void {
  try {
    window.localStorage.setItem(PAGE_SIZE_KEY, String(size));
  } catch {
    /* the choice holds for this visit and no longer */
  }
}

export const allOffered = (total: number): boolean => total < ALL_BELOW;

/** The window of rows one page shows. `page` is zero-based and is clamped, so a
 *  list that shrank under a filter never lands past its last page. */
export function pageWindow(total: number, choice: PageChoice, page: number): { start: number; end: number; page: number; pages: number } {
  if (choice === "all" || total === 0) return { start: 0, end: total, page: 0, pages: 1 };
  const pages = Math.max(1, Math.ceil(total / choice));
  const at = Math.min(Math.max(0, page), pages - 1);
  return { start: at * choice, end: Math.min(total, (at + 1) * choice), page: at, pages };
}
