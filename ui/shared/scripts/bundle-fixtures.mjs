// Strings that only the mock API and its fixtures contain. If any of them is
// in a production bundle, test data is shipping to operators (#1216).
export const FIXTURE_MARKERS = [
  "aop_1200_",
  "prod-db-01.internal",
  "billing-db.internal",
  "Production PostgreSQL",
  "Auth service config",
  "cid_4ad812",
  "aop_validator_fixture",
  "aop_default_policy_fixture",
  "assurance-ingest-failed-scrubbed",
  "assurance-no-validator"
];

/** Returns the markers found in a text blob. */
export function findFixtureMarkers(text) {
  return FIXTURE_MARKERS.filter((m) => text.includes(m));
}
