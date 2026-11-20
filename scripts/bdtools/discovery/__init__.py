"""FR-8's discovery (issue #1541): the readability probe that keeps a
complete but not yet readable source file pending instead of journaling it.

One entry point:

  * `selftest` -- positive controls for the probe's evidence in
    `core/internal/discovery/unreadable_1541_test.go`. A deliberate
    violation is planted in the real product file on a copy of the working
    tree, the narrowed suite runs, and it must fail AND name the promise the
    violation broke.

There is no `check-*.py` here: the Go tests are the gate, and this package
only proves they can go red.
"""
