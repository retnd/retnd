/**
 * The page's side of the Settings anchors (issue #1188). The table of section
 * ids and the link builder live in utilities/routes.ts, next to the other
 * route builders, so code outside the page can link here without importing it.
 */
import { SETTINGS_SECTIONS } from "@shared/utilities/routes";
import type { SettingsSection } from "@shared/utilities/routes";

/** Every current section id, for checking a hash against. */
export const SECTION_IDS: ReadonlySet<string> = new Set(Object.values(SETTINGS_SECTIONS));

/**
 * Ids a section answered to before, mapped to the one it answers to now. The
 * Assurance card was `#assurance-defaults` from #1141 until #1188 renamed it,
 * and links with the old id are in bookmarks and pasted into issues, so the
 * page keeps honouring them.
 */
export const LEGACY_SECTION_IDS: Readonly<Record<string, string>> = {
  "assurance-defaults": SETTINGS_SECTIONS.assurance
};

/**
 * The section a URL hash points at, or null when it names none. A legacy id
 * resolves to its current one, and a hash that does not decode (a lone `%E0`)
 * points at nothing rather than throwing out of the page's effect.
 */
export function sectionForHash(hash: string): string | null {
  let id: string;
  try {
    id = decodeURIComponent(hash.replace(/^#/, ""));
  } catch {
    return null;
  }
  const current = LEGACY_SECTION_IDS[id] ?? id;
  return SECTION_IDS.has(current) ? current : null;
}

/** The attributes that make an element a Settings anchor: its id, and the hook
 *  the stylesheet uses for the scroll margin and the :target highlight. */
export const settingsAnchor = (section: SettingsSection) =>
  ({ id: SETTINGS_SECTIONS[section], "data-settings-section": "" }) as const;
