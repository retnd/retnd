// Package budget is where recovery and scrubbing say what they are allowed
// to spend, and refuse the moment they would spend more (EPIC Q, FR-74).
//
// # A budget is a refusal, not a paragraph
//
// Each limit here is a named constant with a default, a refusal that names
// the budget and the line, an exit code of its own, and a test that drives
// the thing over the line and watches the refusal. A budget with no failing
// case is not a budget. The pattern is the one revalidate already uses for
// money: `placement.Class.CostsEgress()` is read at the moment of spend, and
// the answer is no, not a warning.
//
// # What is host and what is set
//
// Every limit is a property of the host or the process, never of one backup
// set: one staging filesystem and one HTTP server cannot honour a per-set
// override. So there is no per-set field anywhere in Limits, and a host
// (through apps/common/platform/capabilities) may adjust a limit for every
// set at once and no further.
//
// # What it does not do
//
// It renders no cost in currency. Nothing here computes a price, and a field
// that existed would be filled with a plausible guess that somebody then
// budgets against.
package budget
