"""EPIC N and EPIC T's encryption guards (#998, #1320): the falsifications
the two conformance matrices cite.

One entry point:

  * `selftest` -- every planted violation a `PASS` row of
    docs/conformance/epic-n-matrix.md or docs/conformance/epic-t-matrix.md
    cites by its label. Each one is planted in a real product file, the
    narrowed test has to go red naming the test the row cites, and the same
    test has to go green again on the unplanted file.

core/tests/encryptiongate/matrix_test.go reads this file's labels: a `PASS`
row whose quoted label is not in here is refused.
"""
