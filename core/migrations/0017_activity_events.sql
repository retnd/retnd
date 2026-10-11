-- 0017_activity_events: the durable half of the activity feed (EPIC Q, the
-- owner's decision on "are Activity events durable": yes).
--
-- WHY THIS EXISTS. The feed the operation panel and the dashboard strips read
-- (service/liveactivity.go) was a bounded in-memory ring per backup set. It
-- was right to be in memory for the numbers that are only true for the instant
-- they were sampled (a transfer's rate), and wrong for the events: a restart
-- emptied the feed, so a scrub that failed an hour before a deploy, or a
-- recovery that was running when the process died, left no trace on the
-- screen that is built to show it. The UI's reload and reconnect handling
-- (useOperation.ts) was written as if the lines were durable. Now they are.
--
-- WHAT IS STORED. One row per feed event worth keeping, with the sequence the
-- feed gave it. seq is the table's primary key and the feed's cursor, and the
-- feed seeds its counter from MAX(seq) at start, so a sequence never repeats
-- across a restart and a client's "everything after N" keeps meaning what it
-- meant. The ring is refilled from the newest rows of each bucket at start;
-- the table is also what a client pages BACKWARDS through when it asks for
-- events older than the ring holds.
--
-- WHAT IS NOT STORED (and why, because write volume is a decision):
--   * periodic samples (transfer_stats, snapshot_stats): they repeat while a
--     transfer runs and say nothing the next one does not;
--   * the per-range audit line of a streamed download ("recovery range"): a
--     browser can ask for hundreds of ranges, and the download's start, its
--     refusals and its final verification are the rows worth keeping;
--   * recovery_stage lines are COALESCED, not dropped: one row per operation,
--     replaced by each newer stage, so a recovery costs a start row, one stage
--     row naming the last stage it reached, and an end row, however many
--     stages it passes through. A recovery that died shows where it stopped.
--
-- HOW IT IS BOUNDED. A row is kept for 30 days and the table for 50,000 rows,
-- whichever bites first, oldest first. The service prunes at start, then
-- hourly, then again after each 5,000 inserts. The numbers live in Go
-- (service/liveactivity_durable.go), not here, so changing them is not a
-- migration. Per-row sizes are bounded by the writer (message 2 KiB, fields
-- 8 KiB).
--
-- WHY NO CHECK ON event, level, scope OR result. Their vocabularies are the
-- emitter's (internal/obs) and they grow; 0016's header says what a CHECK on
-- a growing list costs. The table is additive: nothing existing changes.
--
-- op_id is the operation id a recovery, a recovery test or a cycle carries in
-- its fields, lifted into a column so "everything that happened in this
-- operation" is one indexed read.
CREATE TABLE activity_events (
    seq       INTEGER PRIMARY KEY,
    at        TEXT NOT NULL,
    level     TEXT NOT NULL,
    event     TEXT NOT NULL,
    scope     TEXT NOT NULL,
    set_id    TEXT NOT NULL DEFAULT '',
    op_id     TEXT NOT NULL DEFAULT '',
    message   TEXT NOT NULL DEFAULT '',
    result    TEXT NOT NULL DEFAULT '',
    action    TEXT NOT NULL DEFAULT '',
    action_id TEXT NOT NULL DEFAULT '',
    fields    TEXT NOT NULL DEFAULT '[]'
);

-- A bucket's newest events, and the backwards page.
CREATE INDEX idx_activity_events_set ON activity_events (set_id, seq);

-- The retention sweep.
CREATE INDEX idx_activity_events_at ON activity_events (at);

-- One operation's lines, and the recovery_stage coalescing.
CREATE INDEX idx_activity_events_op ON activity_events (op_id) WHERE op_id <> '';
