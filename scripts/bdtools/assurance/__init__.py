"""EPIC Q's assurance claims (#988): the guards behind the three claims.

One entry point:

  * `selftest` -- positive controls for the guards that keep Stored,
    Content verified and Recovery tested apart: an existence check that
    cannot become content verification, a recovery test that cannot pass
    without the digest matching, a copy nobody looked at that cannot read
    as fresh, a per-set override that inherits what it does not name, a
    feed that cannot grow a row per copy, and a crash that cannot leave
    staging behind. Every one is planted as a real violation in a real
    product file and the narrowed suite has to go red naming the test.

`scripts/recovery/selftest.sh` (#976) does the same for the recovery
route, the grant and the budgets; this is the other half.
"""
