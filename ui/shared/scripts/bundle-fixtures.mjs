// Strings that only the mock API and its fixtures contain. If any of them is
// in a production bundle, test data is shipping to operators (#1216).
// Only strings that exist nowhere but the fixtures: host names and example
// text that the field help and the set wizard legitimately ship (for example
// prod-db-01.internal, "Production PostgreSQL") are not markers.
export const FIXTURE_MARKERS = [
  "aop_1200_",
  "billing-db.internal",
  "Auth service config",
  "cid_4ad812",
  "aop_validator_fixture",
  "aop_default_policy_fixture",
  "assurance-ingest-failed-scrubbed",
  "assurance-no-validator",
  // #1203: the validators mock and its scenarios.
  "cid_mockvalidator",
  "billing-dump",
  // #1230: the script-state scenarios and the validators only they hold.
  "validators-state-edited",
  "validators-state-missing",
  "restore-log",
  "edited on the host after it was uploaded"
];

/** Returns the markers found in a text blob. */
export function findFixtureMarkers(text) {
  return FIXTURE_MARKERS.filter((m) => text.includes(m));
}
