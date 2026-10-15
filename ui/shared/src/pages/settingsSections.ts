/**
 * Every section of the Settings page, and the anchor it answers to (issue #1188).
 *
 * One table, read by the page that draws the sections and by every link that
 * points into one, so a link cannot name an id the page no longer has. The ids
 * are stable on purpose: they are in people's bookmarks, in the CLI's output and
 * in the chips on other pages, and renaming one is a breaking change.
 */
export const SETTINGS_SECTIONS = {
  serviceBehaviour: "service-behaviour",
  backupDefaults: "backup-defaults",
  capacity: "capacity",
  retention: "retention",
  storageDestinations: "storage-destinations",
  workflow: "workflow",
  assurance: "assurance",
  notifications: "notifications",
  interface: "interface",
  catalogRecovery: "catalog-recovery",
  platform: "platform",
  systemInformation: "system-information",
  password: "password",
  accountRecovery: "account-recovery"
} as const;

export type SettingsSection = keyof typeof SETTINGS_SECTIONS;

/** The in-app path of a Settings section. */
export const settingsLink = (section: SettingsSection): string => "/settings#" + SETTINGS_SECTIONS[section];

/** The attributes that make an element a Settings anchor: its id, and the hook
 *  the stylesheet uses for the scroll margin and the :target highlight. */
export const settingsAnchor = (section: SettingsSection) =>
  ({ id: SETTINGS_SECTIONS[section], "data-settings-section": "" }) as const;
