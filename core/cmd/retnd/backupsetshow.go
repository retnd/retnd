package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/service"
)

// `backup-set show <source/backup-set> [--json]` and `sources --json`
// (issue #1151, EPIC S).
//
// # What was missing
//
// `sources` prints six of about thirty fields a BackupSet carries, and
// nothing printed one set whole. So the trusted host keys, the key id,
// whether the connection was ever proven, both poll intervals, the engine
// and its lineage key, the repository domain and the verification budget
// could be read in a browser and nowhere on a terminal. A script that
// wanted any of them had to parse config.yaml itself, which is the second
// reader of a file that already has one.
//
// # Where the answer comes from
//
// The same place `sources` answers from: this host's configuration, with
// the engine asked afterwards whether it holds the same backup sets
// (readmode.go). That makes show a pure configuration read, and a read
// that disagrees with a serving engine is refused rather than printed, for
// the reason #535 gives.
//
// # --json is the wire object
//
// `--json` prints the object the API returns for the same call:
// BackupSet for show, ListBackupSetsResponse for sources. The service
// types carry no json tags, so printing one would put Go field names on
// stdout that no API client has ever seen; backupSetToWire is the
// projection, and it is the same mapping the handlers apply, field for
// field, down to which fields are omitted when empty.

// cmdBackupSetShow is the `show` verb. It has a flag set of its own, like
// the two toggles beside it, because it takes one id and --json and
// nothing else: parsing it against declareBackupSetFlags would let
// `backup-set show a/b --host x` parse, print the set, and look as though
// it had changed something.
func cmdBackupSetShow(args []string) int {
	fs, cfgPath := newFlagSet("backup-set show")
	asJSON := fs.Bool("json", false, "emit this backup set as the API's BackupSet object instead of as text")

	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	if len(operands) != 2 || operands[0] != "show" {
		return usageError(`backup-set show: expected "show <source/backup-set>" and exactly one backup set id`)
	}
	id := operands[1]
	if _, _, ok := splitBackupSetID(id); !ok {
		return usageError("backup-set show: %q is not a backup set id; a backup set id is exactly source/name", id)
	}

	ctx := context.Background()
	sets, err := readConfiguredBackupSets(ctx, *cfgPath)
	if err != nil {
		return fail(err)
	}
	for _, set := range sets {
		if set.ID != id {
			continue
		}
		if *asJSON {
			return printWorkflowJSON(backupSetToWire(set))
		}
		printBackupSetDetail(set)
		return 0
	}
	return fail(fmt.Errorf("%w: %s", service.ErrBackupSetNotFound, id))
}

// readConfiguredBackupSets is the read both `backup-set show` and
// `sources --json` make: every backup set this host's configuration
// declares, in the service layer's own shape, after the serving engine (if
// there is one) has been asked whether it holds the same sets.
//
// The service log goes to standard error. Standard output is the answer,
// and a JSON document with a log line in front of it is not one a script
// can parse.
func readConfiguredBackupSets(ctx context.Context, configPath string) ([]service.BackupSet, error) {
	cfg, err := config.LoadAndValidate(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	mode, err := enterReadMode(ctx, configPath, cfg, os.Stderr)
	if err != nil {
		return nil, err
	}

	svc, cleanup, err := openBackupServiceLoggingTo(ctx, configPath, readsConfig, os.Stderr)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	sets, err := svc.ListBackupSets(ctx)
	if err != nil {
		return nil, err
	}

	// Asked before anything is printed and about every set at once, for
	// the reason cmdSources gives: a listing that printed correct rows and
	// then refused would already have shown the operator a world the
	// serving process does not have.
	mine := make([]string, 0, len(sets))
	for _, s := range sets {
		mine = append(mine, describeBackupSet(s.ID, s.Disabled, s.ReadOnly))
	}
	if err := mode.agreeOnBackupSets(ctx, mine); err != nil {
		return nil, err
	}
	return sets, nil
}

// printSourcesJSON is `sources --json`: ListBackupSetsResponse.
func printSourcesJSON(ctx context.Context, configPath string) int {
	sets, err := readConfiguredBackupSets(ctx, configPath)
	if err != nil {
		return fail(err)
	}
	out := apicontract.ListBackupSetsResponse{BackupSets: make([]apicontract.BackupSet, 0, len(sets))}
	for _, s := range sets {
		out.BackupSets = append(out.BackupSets, backupSetToWire(s))
	}
	return printWorkflowJSON(out)
}

// backupSetToWire is the wire projection of a backup set: the mapping
// apps/common/webhost applies to build its response, kept here because
// this package may not import the web host.
//
// Two things in it are deliberate. poll_interval_seconds stays null for a
// set that inherits, because null is a real answer and a zero is a request
// to inherit. And the fields only an incremental set can have are left to
// the contract's own omitempty, so an artifact set shows none of them
// instead of showing a zero that reads as a configured budget of none.
func backupSetToWire(s service.BackupSet) apicontract.BackupSet {
	var trusted []apicontract.TrustedHostKey
	for _, k := range s.TrustedHostKeys {
		trusted = append(trusted, apicontract.TrustedHostKey{Algorithm: k.Algorithm, Fingerprint: k.Fingerprint})
	}
	recordedAt := ""
	if !s.TrustedHostKeyRecordedAt.IsZero() {
		recordedAt = s.TrustedHostKeyRecordedAt.UTC().Format(time.RFC3339)
	}
	var override *int
	if s.PollInterval != nil {
		seconds := int(*s.PollInterval / time.Second)
		override = &seconds
	}
	return apicontract.BackupSet{
		ID:                  s.ID,
		SourceName:          s.SourceName,
		Name:                s.Name,
		Host:                s.Host,
		Port:                s.Port,
		User:                s.User,
		RemotePath:          s.RemotePath,
		LocalPath:           s.LocalPath,
		Include:             s.Include,
		CompletionStrategy:  s.CompletionStrategy,
		StableForSeconds:    int(s.StableFor / time.Second),
		StaleAfterSeconds:   int(s.StaleAfter / time.Second),
		ValidatorID:         string(s.ValidatorID),
		SSHKeyID:            s.SSHKeyID,
		Disabled:            s.Disabled,
		ReadOnly:            s.ReadOnly,
		RetentionIsOverride: s.RetentionIsOverride,

		PollIntervalSeconds:          override,
		EffectivePollIntervalSeconds: int(s.EffectivePollInterval / time.Second),

		TrustedHostKeys:          trusted,
		TrustedHostKeyRecordedAt: recordedAt,
		ConnectionUnverified:     s.ConnectionUnverified,

		Engine:                               s.Engine,
		Uuid:                                 s.UUID,
		RepositoryDomain:                     s.RepositoryDomain,
		SourceConsistency:                    s.SourceConsistency,
		VerificationLevel:                    s.VerificationLevel,
		VerificationSamplePercent:            s.VerificationSamplePercent,
		VerificationFullEverySeconds:         int64(s.VerificationFullEvery / time.Second),
		VerificationRestoreDrillEverySeconds: int64(s.VerificationRestoreDrillEvery / time.Second),
		SourceMountPrefix:                    s.SourceMountPrefix,
	}
}

// printBackupSetDetail is `backup-set show` in text: every field the
// BackupSet carries, one `key: value` line each, in the spelling the wire
// uses for the key.
//
// Unlike printBackupSet, which confirms an edit and stays quiet about what
// it cannot know, this prints the lot. connection_unverified is therefore
// printed as true or false: on a read, "false" is the complete statement
// of what the configuration says, and the line that explains what to do
// about a true one is printBackupSet's.
//
// The fields only an incremental set has are printed only when set, which
// is the wire's own rule (omitempty) and for the same reason: an artifact
// set has no lineage, no repository and no verification budget, and a
// blank line for each would read as a configured nothing.
func printBackupSetDetail(s service.BackupSet) {
	fmt.Printf("backup set: %s\n", s.ID)
	fmt.Printf("  host: %s\n", s.Host)
	fmt.Printf("  port: %d\n", s.Port)
	fmt.Printf("  user: %s\n", s.User)
	fmt.Printf("  remote_path: %s\n", s.RemotePath)
	fmt.Printf("  local_path: %s\n", s.LocalPath)
	fmt.Printf("  include: %s\n", strings.Join(s.Include, ", "))
	fmt.Printf("  completion_strategy: %s\n", s.CompletionStrategy)
	fmt.Printf("  stable_for: %s\n", s.StableFor)
	fmt.Printf("  stale_after: %s\n", s.StaleAfter)
	fmt.Printf("  validator_id: %s\n", string(s.ValidatorID))
	fmt.Printf("  disabled: %v\n", s.Disabled)
	fmt.Printf("  read_only: %v\n", s.ReadOnly)
	fmt.Printf("  retention_is_override: %v\n", s.RetentionIsOverride)

	// The key reference is an id, never a path (SSHKeyRef's rule), and
	// the trusted keys are fingerprints, never key material.
	fmt.Printf("  ssh_key_id: %s\n", s.SSHKeyID)
	if len(s.TrustedHostKeys) == 0 {
		fmt.Println("  trusted_host_key: none on record")
	}
	for _, k := range s.TrustedHostKeys {
		fmt.Printf("  trusted_host_key: %s %s\n", k.Algorithm, k.Fingerprint)
	}
	if !s.TrustedHostKeyRecordedAt.IsZero() {
		fmt.Printf("  trusted_host_key_recorded_at: %s\n", s.TrustedHostKeyRecordedAt.UTC().Format(time.RFC3339))
	}
	fmt.Printf("  connection_unverified: %v\n", s.ConnectionUnverified)

	fmt.Printf("  poll_interval: %s\n", pollIntervalWord(s))
	fmt.Printf("  effective_poll_interval: %s\n", s.EffectivePollInterval)

	fmt.Printf("  engine: %s\n", s.Engine)
	for _, f := range []struct{ key, value string }{
		{"uuid", s.UUID},
		{"repository_domain", s.RepositoryDomain},
		{"source_consistency", s.SourceConsistency},
		{"verification_level", s.VerificationLevel},
	} {
		if f.value != "" {
			fmt.Printf("  %s: %s\n", f.key, f.value)
		}
	}
	if s.VerificationSamplePercent != 0 {
		fmt.Printf("  verification_sample_percent: %d\n", s.VerificationSamplePercent)
	}
	if s.VerificationFullEvery != 0 {
		fmt.Printf("  verification_full_every: %s\n", s.VerificationFullEvery)
	}
	if s.VerificationRestoreDrillEvery != 0 {
		fmt.Printf("  verification_restore_drill_every: %s\n", s.VerificationRestoreDrillEvery)
	}
	if s.SourceMountPrefix != "" {
		fmt.Printf("  source_mount_prefix: %s\n", s.SourceMountPrefix)
	}
}

// pollIntervalWord is a set's own cadence as one word: the override, or
// "inherited" when the set follows the deployment's. It never prints the
// effective number in the override's place, because a surface that did
// would make the next save an explicit override of a value the set was
// only tracking (#845).
func pollIntervalWord(s service.BackupSet) string {
	if s.PollInterval == nil {
		return "inherited"
	}
	return s.PollInterval.String()
}
