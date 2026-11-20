// Package logpipeline executes the shipped Promtail and Alloy pipelines over
// a captured log file and asserts the Loki label set that comes out
// (EPIC O, FR-100, matrix row O-08).
//
// It is executed rather than inspected on purpose. A test that greps the
// YAML for action_id passes against a config that promotes event instead,
// and the property is the resulting label SET, not the absence of one
// string. So this runs the real binaries (grafana/promtail and grafana/alloy
// in containers), points them at a log file written by the real obs.Logger,
// receives what they push to Loki, and reads the labels off the wire.
//
// It needs a Docker daemon and network access to pull the two images, and
// it fails rather than skips without them: a guard that skips itself when
// it cannot run is the guard that cannot fail. Because of that it sits
// behind a build tag and runs from scripts/observability/log-pipeline.sh,
// not from a plain `go test ./...`.
package logpipeline
