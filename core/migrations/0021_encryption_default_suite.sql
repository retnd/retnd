-- 0021_encryption_default_suite: the build default suite this deployment
-- last ran under (EPIC T, #1314; ADR 0033 D4).
--
-- WHY THIS EXISTS. A new build may move copycrypt.Default() (age-pq/v1 since
-- #1313). It never rotates a key because of that: a deployment with an
-- active key keeps that key's suite. What it owes the operator is one
-- activity event saying the build default moved, emitted once per change and
-- never on a steady build, and "once" needs the previous value to survive a
-- restart. The activity feed is pruned, so it cannot be that record.
--
-- ONE ROW. id is pinned to 1. Absence means this deployment has never run a
-- build that records it, which internal/state reads as EPIC N's implicit
-- default (age/v1) when the deployment has ever held a key, and as nothing
-- to announce otherwise.
--
-- NO SECRET. A suite id and a time.

CREATE TABLE encryption_default_suite (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    suite_id    TEXT NOT NULL CHECK (suite_id <> ''),
    recorded_at TEXT NOT NULL
);
