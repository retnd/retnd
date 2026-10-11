# Changelog

## [Unreleased]

### Changed

- **The "no download" promise is reversed, and the boundary sentence replaces it** (#987,
  EPIC Q #953). This product recovers and exports the retained artifact; restoring it into
  an application remains the operator's responsibility. The old claim was made
  independently in several places (`docs/recovery.md`, two paragraphs and a note on
  `docs/site/reference.html`, `docs/site/index.html#honest`, the store listing copy) and
  all of them now say the new thing rather than a softer version of the old one. What the
  deleted claim was split into, and where each piece landed, is recorded item by item in
  [ADR 0029](docs/adr/0029-the-recovery-boundary.md). The documents that carry the new
  material are `docs/recovery.md` (the three restore-shaped verbs side by side, recovering
  an artifact, the three claims, scrubbing and recovery tests, what to do when the catalog
  itself is gone, and troubleshooting), `docs/recovery-without-a-terminal.md` (recovery
  as its own numbered failure-and-fix section), `docs/storage-mediums.md`,
  `docs/runtime-contract.md`, `docs/incremental-runbooks.md` and
  `docs/compliance/privacy-policy.md`.
- **One thing to know before you need it: the `assurance:` block is a one-way door.** The
  configuration loader rejects unknown keys, so a `config.yaml` that carries the new
  `assurance:` block (globally or on a backup set) is refused outright by a build that
  predates it, with a message naming the key. An existing deployment that adds nothing sees
  no change: the global default is good enough to leave alone, and a configuration that
  mentions nothing new loads and scrubs on that default. Add the block while things are
  calm, because the moment you reach for this feature is usually an incident, and a
  downgrade after adding it fails closed...

