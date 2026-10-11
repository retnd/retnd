# Changelog

## [Unreleased]

### Changed

- **A fresh install configures itself and leads you through setup** (#1049). On its
  first start, with no `config.yaml` in the configuration directory, the engine now
  writes a small default one (the documented one hour polling interval, the journal
  path it was started with, no sources), created exclusively with mode 0600 and never
  over an existing file, and says so once at INFO. An empty `sources` list is valid, an
  absent configuration is no longer an ERROR line, and `retnd status` and `retnd check`
  on a fresh install print a first-run notice with the next step and exit 0 (`check`
  still fails, naming the directory, when the configuration directory is not writable).
  The Settings page shows the engine's defaults, a first-run guide replaces the old
  one-paragraph banner (administrator, email, first backup set, each skippable), and
  enrolment no longer needs a mail server. "Copy details" now works on a plain-http
  origin by falling back to the browser's older copy command and then to selecting the
  text, and only says Copied when it did.

  **Rolling back a never-configured install:** an older release refuses a configuration
  with no backup set, so delete the default `config.yaml` (or add a backup set first)
  before starting the older binary. An install that already has a configuration is not
  touched by any of this.

## [0.5.1] - 2026-10-04

0.5.1 is 0.5.0 plus the one logo. 0.5.0 was published without it: the logo
rework (#1019) landed on `main` after the 0.5.0 image inputs were recorded, and
it changed an image input (`ui/shared`), so shipping it means a new image and
re-recorded hashes rather than an edit to a published tag (#1042).

