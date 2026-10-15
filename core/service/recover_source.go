package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/transport"
)

// The "Source host (SFTP)" recovery destination (issues #1191 and #1193): a
// recovered backup goes back where it came from, an SFTP upload onto the
// host the set pulls from, over the set's own settings.
//
// # Why it is the default, and where it is not
//
// Sending a backup home is what an operator recovering one usually wants, so
// the dialogs start on it and `retnd recover` with no --to means it. It is
// also the one recovery that writes onto a production machine, so the
// default only applies where it can work: a set with no SFTP source, or one
// declared read_only, has no default (DefaultRecoveryTarget says why), and
// the default is never implied consent.
//
// # Describe on open, check on intent
//
// DescribeSourceHost reads configuration and nothing else. The write check
// (CheckSourceHostWrite, #852's probe: write one file, remove it, confirm it
// is gone) only runs when somebody asks for it, is cached per set for five
// minutes behind a single flight, is rate limited, and writes an audit row.
// The mint and a set run reuse a fresh verdict, so one recovery costs one
// probe. A probe that could not remove its file means "not writable" here,
// as it does in the connection test, and the set is not probed again until
// that file is gone.
//
// # Consent bound to a destination
//
// The request does not say "yes" in general. It echoes the destination_id
// the description gave it: a digest of host, port, account, remote path,
// folder and host-key file. The mint works the id out again from the
// configuration and refuses a mismatch, the grant carries it, and every
// upload of a run works it out again before it writes, so a reload between
// what the operator read and the write (or in the middle of a long run)
// cannot redirect it, and a set declared read_only mid-run stops the run.
//
// # Where the files go
//
// Into a new folder, retnd-recovered-<date>-<time>-<4 hex>, directly under
// the set's remote path, claimed with a rename that fails when anything is
// already there. internal/discovery skips exactly that depth, so the next
// run never takes a recovered file for a new backup. Each file is written
// under a temporary name and renamed into place by the transport, so a
// dropped connection never leaves a short file under a real name and nothing
// that is already there is ever replaced.

// The source-host refusals and failures.
const (
	// RecoverySourceHostUnavailable: the set has no source host to send a
	// backup back to (not SFTP, an incremental set, no longer configured).
	RecoverySourceHostUnavailable = "RECOVERY_SOURCE_HOST_UNAVAILABLE"
	// RecoverySourceHostReadOnly: the set is declared read_only, or the
	// account it pulls with may not write on the host.
	RecoverySourceHostReadOnly = "RECOVERY_SOURCE_HOST_READ_ONLY"
	// RecoverySourceHostNotAcknowledged: the request did not acknowledge
	// the destination, or acknowledged one the configuration no longer
	// resolves to.
	RecoverySourceHostNotAcknowledged = "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED"
	// RecoverySourceHostUnreachable: retnd could not reach the host to check
	// it or to write there. Transient, and worth trying again.
	RecoverySourceHostUnreachable = "RECOVERY_SOURCE_HOST_UNREACHABLE"
	// RecoverySourceHostNoSpace: what would be written does not fit in the
	// host's free space less a reserve.
	RecoverySourceHostNoSpace = "RECOVERY_SOURCE_HOST_NO_SPACE"
	// RecoverySourceHostProbeLeftFile: a write check wrote its probe file
	// and could not remove it, so the host is not treated as writable.
	RecoverySourceHostProbeLeftFile = "RECOVERY_SOURCE_HOST_PROBE_LEFT_FILE"
)

// What a write check found.
const (
	SourceWriteNotChecked    = "not_checked"
	SourceWriteWritable      = "writable"
	SourceWriteReadOnly      = "read_only"
	SourceWriteUnreachable   = "unreachable"
	SourceWriteProbeLeftFile = "probe_left_file"
)

const (
	// maxRecoveredFolderLen bounds a folder name an operator types.
	maxRecoveredFolderLen = 128
	// sourceWriteCacheTTL is how long a write check's verdict stands.
	sourceWriteCacheTTL = 5 * time.Minute
	// sourceWriteChecksPerMinute bounds the probes this process runs a
	// minute across every set. A cached answer is never counted.
	sourceWriteChecksPerMinute = 6
	// sourceGrantTTL is a source-host grant's lifetime.
	sourceGrantTTL = 60 * time.Second
	// sourceProbeTimeout bounds one shared probe, which runs on its own
	// context so one caller giving up does not fail the others waiting.
	sourceProbeTimeout = 90 * time.Second
)

// RecoveryTargetWritesToSourceHost is the one place that says whether a
// recovery target writes onto a backup set's own source host. The HTTP gate
// asks this rather than comparing kinds itself, and a test holds the service
// to it: nothing the service accepts writes onto a source host unless this
// says so.
func RecoveryTargetWritesToSourceHost(t RecoveryTarget) bool {
	return t.Kind == string(export.TargetSource)
}

// SourceHostDescription is one backup set's source-host destination, from
// configuration, plus the last write check's answer.
type SourceHostDescription struct {
	SetID string

	// Available is false when the set has no source host to send a backup
	// back to; Reason then says why, in a sentence for the operator.
	Available bool
	Reason    string

	// Where it writes: the set's own SFTP settings, never the caller's.
	Host       string
	Port       int
	User       string
	RemotePath string
	// knownHosts is the host-key file the set trusts, part of the identity.
	knownHosts string

	// Folder is the new folder this description is for.
	Folder string

	// ReadOnly is a set declared read_only: never written, never probed.
	ReadOnly bool

	// WriteAccess is one of the SourceWrite* answers; Code is the stable
	// code for the three that refuse, and Detail the sentence.
	WriteAccess string
	Code        string
	Detail      string
	CheckedAt   time.Time
	// LeftFile is the probe file to delete, for SourceWriteProbeLeftFile.
	LeftFile string
	// FreeBytes is the free space under the remote path, when known.
	FreeBytes *int64
}

// URL is the destination as the engine will write it.
func (d SourceHostDescription) URL() string {
	if !d.Available {
		return ""
	}
	host := d.Host
	if d.Port != 0 && d.Port != 22 {
		host = fmt.Sprintf("%s:%d", d.Host, d.Port)
	}
	return "sftp://" + d.User + "@" + host + path.Join("/", d.RemotePath, d.Folder) + "/"
}

// DestinationID is the digest a request acknowledges this destination with.
func (d SourceHostDescription) DestinationID() string {
	if !d.Available || d.Folder == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"retnd-source-host-v1", strings.ToLower(d.Host), strconv.Itoa(d.Port), d.User,
		path.Clean("/" + d.RemotePath), d.Folder, d.knownHosts,
	}, "\x00")))
	return "sh1-" + hex.EncodeToString(sum[:16])
}

// connectionKey is the destination without its folder: what a write check's
// verdict is about.
func (d SourceHostDescription) connectionKey() string {
	return strings.Join([]string{d.SetID, strings.ToLower(d.Host), strconv.Itoa(d.Port), d.User, path.Clean("/" + d.RemotePath), d.knownHosts}, "\x00")
}

// defaultRecoveredFolder is a new folder name: the time to the second and
// four random hex digits, so two recoveries started in one second do not
// pick the same folder (and the claim would refuse the second if they did).
func defaultRecoveredFolder() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return transport.RecoveredFolderPrefix + now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

var recoveredFolderRest = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// validRecoveredFolder is the rule a folder named by a caller meets: one
// segment carrying the prefix discovery skips, then letters, digits, '.',
// '_' and '-', not starting with a dot (the transport's own claim names do)
// and not ending in one (a trailing dot is silently dropped by some file
// systems, so the folder would not be the one named).
func validRecoveredFolder(folder string) bool {
	if len(folder) > maxRecoveredFolderLen || !strings.HasPrefix(folder, transport.RecoveredFolderPrefix) || strings.HasSuffix(folder, ".") {
		return false
	}
	return recoveredFolderRest.MatchString(strings.TrimPrefix(folder, transport.RecoveredFolderPrefix))
}

// configuredSource finds a set and the source it is declared under.
func configuredSource(sources []config.Source, id model.BackupSetID) (config.Source, config.BackupSet, bool) {
	for _, src := range sources {
		for _, bs := range src.BackupSets {
			if bs.ID == id {
				return src, bs, true
			}
		}
	}
	return config.Source{}, config.BackupSet{}, false
}

// setIDOf reads a set id out of either a set id or an artifact id.
func setIDOf(id string) (model.BackupSetID, bool) {
	if a, err := app.ParseArtifactID(id); err == nil {
		return a.Set, true
	}
	if s, err := model.ParseBackupSetID(id); err == nil {
		return s, true
	}
	return model.BackupSetID{}, false
}

// resolvedSourceHost is a description together with what it was built from.
type resolvedSourceHost struct {
	d   SourceHostDescription
	cfg *config.Config
	src config.Source
	bs  config.BackupSet
}

func (r resolvedSourceHost) transportSource() transport.Source {
	return app.SourceFor(r.cfg, r.src, r.bs)
}

// resolveSourceHost builds the description of setID's source host for
// folder ("" suggests a new one) from the configuration as it is now. An
// unknown set is ErrBackupSetNotFound, an invalid folder a
// RECOVERY_TARGET_INVALID refusal.
func (b *BackupService) resolveSourceHost(setID, folder string) (resolvedSourceHost, error) {
	id, ok := setIDOf(setID)
	if !ok {
		return resolvedSourceHost{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, setID)
	}
	cfg := b.state.Load().inner.Config
	src, bs, found := configuredSource(cfg.Sources, id)
	if !found {
		return resolvedSourceHost{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	}
	if folder == "" {
		folder = defaultRecoveredFolder()
	} else if !validRecoveredFolder(folder) {
		return resolvedSourceHost{}, recoveryErr(RecoveryTargetInvalid,
			"a source-host recovery goes into a new folder directly under the set's remote path, named %s followed by letters, digits, '.', '_' or '-' and not ending in a dot (the next backup run skips those), and %q is not one", transport.RecoveredFolderPrefix, folder)
	}
	r := resolvedSourceHost{cfg: cfg, src: src, bs: bs}
	d := SourceHostDescription{SetID: id.String(), Folder: folder, WriteAccess: SourceWriteNotChecked}
	switch {
	case bs.Engine == model.EngineKopia:
		d.Reason = "this is an incremental (kopia) set: its backups are restore points in a repository, not files, so they come back with `" + recoverVerb + " snapshot restore` and there is no source-host upload for it"
	case bs.Remote.Type != "sftp":
		kind := bs.Remote.Type
		if kind == "local" {
			kind = "local folder"
		}
		d.Reason = "this set's source is a " + kind + ", not SFTP, so there is no source host to send the backup back to; choose another destination"
	default:
		d.Available = true
		d.Host, d.Port, d.User, d.RemotePath, d.knownHosts = bs.Remote.Host, bs.Remote.Port, bs.Remote.User, bs.RemotePath, bs.Remote.KnownHosts
		if d.Port == 0 {
			d.Port = 22
		}
		if bs.ReadOnly {
			d.ReadOnly = true
			d.WriteAccess, d.Code = SourceWriteReadOnly, RecoverySourceHostReadOnly
			d.Detail = "this set is declared read_only (pull from here, never change anything here), so retnd will not write onto " + d.Host + "; choose another destination"
		}
	}
	if !d.Available {
		d.Folder = ""
	}
	r.d = d
	return r, nil
}

// DescribeSourceHost is setID's source-host destination for folder ("" for a
// fresh one), from configuration and the cached write check. It never
// touches the network.
func (b *BackupService) DescribeSourceHost(setID, folder string) (SourceHostDescription, error) {
	r, err := b.resolveSourceHost(setID, folder)
	if err != nil {
		return SourceHostDescription{}, err
	}
	if r.d.Available && !r.d.ReadOnly {
		if v, ok := b.sourceHosts.cached(r.d.connectionKey()); ok {
			v.applyTo(&r.d)
		}
	}
	return r.d, nil
}

// DefaultRecoveryTarget is the destination a recovery of this artifact or
// set starts on where the source host can take it: the source host, in a
// new folder, unacknowledged. Otherwise the zero target and why, so the
// caller can name the alternatives. Configuration only.
func (b *BackupService) DefaultRecoveryTarget(id string) (RecoveryTarget, string) {
	r, err := b.resolveSourceHost(id, "")
	switch {
	case err != nil:
		return RecoveryTarget{}, "there is no such backup set in this configuration"
	case !r.d.Available:
		return RecoveryTarget{}, r.d.Reason
	case r.d.ReadOnly:
		return RecoveryTarget{}, "this set is declared read_only, so retnd never writes onto its source host"
	}
	return RecoveryTarget{Kind: string(export.TargetSource), Path: r.d.Folder}, ""
}

// SourceWriteCheckRateLimited refuses a write check over the per-minute
// budget. A cached answer is never refused.
type SourceWriteCheckRateLimited struct{ RetryAfter time.Duration }

func (e *SourceWriteCheckRateLimited) Error() string {
	return fmt.Sprintf("service: too many source-host write checks in the last minute; try again in %s", e.RetryAfter.Round(time.Second))
}

// CheckSourceHostWrite answers "may retnd write there" for setID's source
// host, by the write probe when no fresh answer is cached. A set declared
// read_only is answered without one. actor is who asked, for the audit row.
func (b *BackupService) CheckSourceHostWrite(ctx context.Context, actor, setID, folder string) (SourceHostDescription, error) {
	r, err := b.resolveSourceHost(setID, folder)
	if err != nil {
		return SourceHostDescription{}, err
	}
	if !r.d.Available {
		return SourceHostDescription{}, recoveryErr(RecoverySourceHostUnavailable, "%s", r.d.Reason)
	}
	if r.d.ReadOnly {
		return r.d, nil
	}
	v, err := b.sourceWriteVerdict(ctx, actor, r, true)
	if err != nil {
		return SourceHostDescription{}, err
	}
	v.applyTo(&r.d)
	return r.d, nil
}

// sourceVerdict is one write check's answer.
type sourceVerdict struct {
	access, code, detail string
	leftFile             string // the probe's name under the remote path
	leftFull             string // the same, as a full path for the operator
	at                   time.Time
	free                 *int64
}

func (v sourceVerdict) applyTo(d *SourceHostDescription) {
	d.WriteAccess, d.Code, d.Detail, d.CheckedAt, d.LeftFile, d.FreeBytes = v.access, v.code, v.detail, v.at, v.leftFull, v.free
}

// refusal is the verdict as the error a recovery is refused with, nil for
// one that may go ahead.
func (v sourceVerdict) refusal() error {
	switch v.access {
	case SourceWriteReadOnly, SourceWriteProbeLeftFile:
		return recoveryErr(v.code, "%s", v.detail)
	case SourceWriteUnreachable:
		return &RecoveryError{Code: RecoverySourceHostUnreachable, Message: v.detail}
	}
	return nil
}

// sourceHostChecks is the write-check cache, single flight and rate limit.
type sourceHostChecks struct {
	mu       sync.Mutex
	verdicts map[string]sourceVerdict
	flights  map[string]*sourceFlight
	limiter  *sourceCheckLimiter
}

type sourceFlight struct {
	done chan struct{}
	v    sourceVerdict
	err  error
}

func (c *sourceHostChecks) cached(key string) (sourceVerdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.verdicts[key]
	if !ok || (v.access != SourceWriteProbeLeftFile && now().Sub(v.at) >= sourceWriteCacheTTL) {
		return sourceVerdict{}, false
	}
	return v, true
}

// sourceCheckLimiter is a sliding minute of probe start times.
type sourceCheckLimiter struct {
	mu     sync.Mutex
	starts []time.Time
}

func newSourceCheckLimiter() *sourceCheckLimiter { return &sourceCheckLimiter{} }

func (l *sourceCheckLimiter) allow(at time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.starts[:0]
	for _, s := range l.starts {
		if at.Sub(s) < time.Minute {
			keep = append(keep, s)
		}
	}
	l.starts = keep
	if len(l.starts) >= sourceWriteChecksPerMinute {
		return time.Minute - at.Sub(l.starts[0]), false
	}
	l.starts = append(l.starts, at)
	return 0, true
}

// sourceWriteVerdict is the cached verdict for r's connection when it is
// fresh, otherwise the answer of one probe that every concurrent caller
// shares. limited counts a new probe against the per-minute budget, which
// the write-check route does and a recovery that has already been asked
// for does not.
func (b *BackupService) sourceWriteVerdict(ctx context.Context, actor string, r resolvedSourceHost, limited bool) (sourceVerdict, error) {
	c := &b.sourceHosts
	key := r.d.connectionKey()
	for {
		c.mu.Lock()
		if c.verdicts == nil {
			c.verdicts, c.flights = map[string]sourceVerdict{}, map[string]*sourceFlight{}
		}
		if c.limiter == nil {
			c.limiter = newSourceCheckLimiter()
		}
		v, have := c.verdicts[key]
		if have && v.access != SourceWriteProbeLeftFile && now().Sub(v.at) < sourceWriteCacheTTL {
			c.mu.Unlock()
			return v, nil
		}
		if f, ok := c.flights[key]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
				if f.err != nil {
					return sourceVerdict{}, f.err
				}
				return f.v, nil
			case <-ctx.Done():
				return sourceVerdict{}, ctx.Err()
			}
		}
		c.mu.Unlock()

		if have && v.access == SourceWriteProbeLeftFile {
			// Not probed again while our own file is still there: another
			// probe would only leave another one.
			if b.sourceLeftFileStillThere(ctx, r, v.leftFile) {
				return v, nil
			}
		}

		c.mu.Lock()
		if _, raced := c.flights[key]; raced {
			c.mu.Unlock()
			continue
		}
		if limited {
			if wait, ok := c.limiter.allow(now()); !ok {
				c.mu.Unlock()
				return sourceVerdict{}, &SourceWriteCheckRateLimited{RetryAfter: wait}
			}
		}
		f := &sourceFlight{done: make(chan struct{})}
		c.flights[key] = f
		c.mu.Unlock()

		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceProbeTimeout)
		f.v, f.err = b.probeSourceHost(pctx, actor, r)
		cancel()

		c.mu.Lock()
		delete(c.flights, key)
		if f.err == nil && f.v.access != SourceWriteNotChecked {
			c.verdicts[key] = f.v
		}
		c.mu.Unlock()
		close(f.done)
		return f.v, f.err
	}
}

// sourceLeftFileStillThere asks the host (a read, never a write) whether a
// probe file a check left is still there. Not knowing counts as yes.
func (b *BackupService) sourceLeftFileStillThere(ctx context.Context, r resolvedSourceHost, name string) bool {
	if name == "" {
		return true
	}
	tr := b.state.Load().inner.Transport
	if tr == nil {
		return true
	}
	_, err := tr.Stat(ctx, r.transportSource(), name)
	if err == nil {
		return true
	}
	cat, _ := transport.CategoryOf(err)
	return cat != transport.NotFound
}

// probeSourceHost runs the write probe, asks for the free space, words what
// it found and writes the audit row.
func (b *BackupService) probeSourceHost(ctx context.Context, actor string, r resolvedSourceHost) (sourceVerdict, error) {
	tr := b.state.Load().inner.Transport
	probe, _ := tr.(transport.SourceWriteProbe)
	v := sourceVerdict{at: now(), access: SourceWriteNotChecked}
	if probe == nil {
		v.detail = "this deployment cannot check ahead whether it may write on " + r.d.Host + ", so the upload itself will find out"
		return v, nil
	}
	src := r.transportSource()
	err := probe.ProbeSourceWrite(ctx, src)
	host := r.d.Host
	var pnr *transport.ProbeNotRemovedError
	switch {
	case err == nil:
		v.access = SourceWriteWritable
		v.detail = "retnd can write on " + host + " as " + r.d.User
		if sp, ok := tr.(transport.SourceSpaceReporter); ok {
			if free, known, ferr := sp.SourceFreeSpace(ctx, src); ferr == nil && known {
				v.free = &free
			}
		}
	case errors.Is(err, transport.ErrProbeNotRemoved):
		v.access, v.code = SourceWriteProbeLeftFile, RecoverySourceHostProbeLeftFile
		if errors.As(err, &pnr) && pnr.Name != "" {
			v.leftFile = pnr.Name
			v.leftFull = path.Join(r.d.RemotePath, pnr.Name)
			v.detail = "retnd wrote a probe file on " + host + " and could not remove it again, so it does not treat this host as one it may write on (the connection test does not either); delete " + v.leftFull + " on " + host + " and check again"
		} else {
			v.detail = "retnd wrote a probe file on " + host + " and could not remove it again, so it does not treat this host as one it may write on; delete the file starting " + transport.ProbeObjectPrefix + " under " + r.d.RemotePath + " and check again"
		}
		b.logger.Error(ctx, "recovery_source_probe", err)
	case ctx.Err() != nil:
		b.auditSourceCheck(ctx, actor, r, v, ctx.Err())
		return sourceVerdict{}, ctx.Err()
	default:
		if cat, ok := transport.CategoryOf(err); ok && cat == transport.PermissionDenied {
			v.access, v.code = SourceWriteReadOnly, RecoverySourceHostReadOnly
			v.detail = fmt.Sprintf("the account retnd pulls with (%s) is read-only on %s, so an upload there will be refused until that account may write under %s; choose another destination, or give it write access", r.d.User, host, r.d.RemotePath)
		} else {
			v.access, v.code = SourceWriteUnreachable, RecoverySourceHostUnreachable
			v.detail = "retnd could not reach " + host + " to check that it may write there, so nothing was written; try again, and the engine log has the cause"
			b.logger.Error(ctx, "recovery_source_probe", err)
		}
	}
	b.auditSourceCheck(ctx, actor, r, v, nil)
	return v, nil
}

// auditSourceCheck writes the action-log row a probe owes: it wrote and
// removed a file on a production host.
func (b *BackupService) auditSourceCheck(ctx context.Context, actor string, r resolvedSourceHost, v sourceVerdict, err error) {
	result, outcome := obs.ResultSuccess, v.access
	if err != nil {
		result, outcome = obs.ResultError, "failed"
	} else if v.access != SourceWriteWritable {
		result = obs.ResultWarn
	}
	attrs := []slog.Attr{
		slog.String("actor", actor),
		slog.String("route", "POST /api/v1/backup-sets/{source}/{set}/source-host/write-check"),
		slog.String("operation", "recovery write-check"),
		slog.String("set", r.d.SetID),
		slog.String("target", "sftp://"+r.d.User+"@"+r.d.Host+":"+strconv.Itoa(r.d.Port)+path.Join("/", r.d.RemotePath)+"/"),
		slog.String("outcome", outcome),
	}
	if v.code != "" {
		attrs = append(attrs, slog.String("code", v.code))
	}
	if v.leftFull != "" {
		attrs = append(attrs, slog.String("left_file", v.leftFull))
	}
	b.logger.Completed(ctx, result, obs.EventAPIAction, "recovery write-check "+outcome, attrs...)
}

// ensureSourceWritable is the gate a source recovery passes before a byte is
// read: the cached verdict, or one probe. A transport with no probe lets the
// upload find out.
func (b *BackupService) ensureSourceWritable(ctx context.Context, actor string, r resolvedSourceHost) error {
	v, err := b.sourceWriteVerdict(ctx, actor, r, false)
	if err != nil {
		return err
	}
	return v.refusal()
}

// checkSourceSpace refuses when need bytes do not fit in the host's free
// space less the staging-style reserve. It returns the free space it found,
// nil when the host cannot say (which is not a refusal; the dialog says so).
func (b *BackupService) checkSourceSpace(ctx context.Context, r resolvedSourceHost, need, reserve int64) (*int64, error) {
	sp, _ := b.state.Load().inner.Transport.(transport.SourceSpaceReporter)
	if sp == nil {
		return nil, nil
	}
	free, known, err := sp.SourceFreeSpace(ctx, r.transportSource())
	if err != nil || !known {
		if err != nil && ctx.Err() == nil {
			b.logger.Error(ctx, "recovery_source_space", err)
		}
		return nil, nil
	}
	if need > free-reserve {
		return &free, recoveryErr(RecoverySourceHostNoSpace,
			"this writes %d bytes onto %s and it has %d bytes free under %s, which would leave less than the %d byte reserve, so nothing was read",
			need, r.d.Host, free, r.d.RemotePath, reserve)
	}
	return &free, nil
}

// validateSourceTarget checks a source target against the set it would
// write to and resolves the folder. name is the file's name for a single
// recovery, empty for a run of several. requireAck is false for a plan,
// which asks what would happen and writes nothing.
func (b *BackupService) validateSourceTarget(setID string, t RecoveryTarget, name string, requireAck bool) (export.Target, resolvedSourceHost, error) {
	r, err := b.resolveSourceHost(setID, t.Path)
	if errors.Is(err, ErrBackupSetNotFound) {
		return export.Target{}, r, recoveryErr(RecoverySourceHostUnavailable, "this set is not in the configuration, so it has no source host")
	}
	if err != nil {
		return export.Target{}, r, err
	}
	d := r.d
	switch {
	case !d.Available:
		return export.Target{}, r, recoveryErr(RecoverySourceHostUnavailable, "%s", d.Reason)
	case d.ReadOnly:
		return export.Target{}, r, recoveryErr(RecoverySourceHostReadOnly, "%s", d.Detail)
	}
	if requireAck {
		if err := checkAcknowledged(d, t.AcknowledgedDestination); err != nil {
			return export.Target{}, r, err
		}
	}
	return export.Target{Kind: export.TargetSource, Path: path.Join(d.Folder, name), Destination: d.DestinationID()}, r, nil
}

// checkAcknowledged is the consent check: the request acknowledged exactly
// this destination.
func checkAcknowledged(d SourceHostDescription, acked string) error {
	switch {
	case acked == "":
		return recoveryErr(RecoverySourceHostNotAcknowledged,
			"this writes onto %s, the host this backup came from, into %s as %s; acknowledge that destination explicitly (target.acknowledged_destination %s in the request, --acknowledge-source-write in the terminal)",
			d.Host, d.URL(), d.User, d.DestinationID())
	case acked != d.DestinationID():
		return recoveryErr(RecoverySourceHostNotAcknowledged,
			"the destination changed since it was acknowledged: it is now %s as %s (host keys from %s), so nothing was written; look at it again and acknowledge what it is now",
			d.URL(), d.User, firstNonEmpty(d.knownHosts, "the default file"))
	}
	return nil
}

// recheckSourceDestination works the destination out again from the
// configuration as it is now, before a write, and refuses when it is not the
// one that was acknowledged, the set is now read_only, or it no longer has a
// source host.
func (b *BackupService) recheckSourceDestination(setID, folder, acked string) (resolvedSourceHost, error) {
	r, err := b.resolveSourceHost(setID, folder)
	switch {
	case errors.Is(err, ErrBackupSetNotFound):
		return r, recoveryErr(RecoverySourceHostUnavailable, "this set is no longer in the configuration, so nothing more was written to its source host")
	case err != nil:
		return r, err
	case !r.d.Available:
		return r, recoveryErr(RecoverySourceHostUnavailable, "%s; nothing more was written", r.d.Reason)
	case r.d.ReadOnly:
		return r, recoveryErr(RecoverySourceHostReadOnly, "this set has been declared read_only since the recovery was acknowledged, so nothing more was written onto %s", r.d.Host)
	}
	return r, checkAcknowledged(r.d, acked)
}

// sourceUpload is one open upload onto a source host: one connection, one
// claimed folder, the destination it was acknowledged for.
type sourceUpload struct {
	b      *BackupService
	setID  string
	folder string
	dest   string
	r      resolvedSourceHost
	sess   transport.SourceUploadSession
}

// beginSourceUpload checks the destination again, opens one session (which
// holds the host's write slot) and claims the folder.
func (b *BackupService) beginSourceUpload(ctx context.Context, setID, folder, dest string) (*sourceUpload, error) {
	r, err := b.recheckSourceDestination(setID, folder, dest)
	if err != nil {
		return nil, err
	}
	up, _ := b.state.Load().inner.Transport.(transport.SourceUploader)
	if up == nil {
		return nil, recoveryErr(RecoverySourceHostUnavailable, "this deployment has no way to write onto a source host")
	}
	sess, err := up.BeginSourceUpload(ctx, r.transportSource())
	if err != nil {
		return nil, b.sourceWriteError(ctx, r, "", err)
	}
	u := &sourceUpload{b: b, setID: setID, folder: folder, dest: dest, r: r, sess: sess}
	if err := sess.ClaimFolder(ctx, folder, false); err != nil {
		_ = sess.Close()
		if errors.Is(err, transport.ErrSourceObjectExists) {
			return nil, recoveryErr(RecoveryTargetExists, "%s already has something at %s, so nothing was written; recovery only ever writes into a folder it has just created", r.d.Host, path.Join(r.d.RemotePath, folder))
		}
		return nil, b.sourceWriteError(ctx, r, "", err)
	}
	return u, nil
}

// put uploads one verified staged file as name in the folder, after
// checking the destination again. It returns the full path on the host and
// how the file was verified there.
func (u *sourceUpload) put(ctx context.Context, staged *export.Staged, name string) (string, string, error) {
	if _, err := u.b.recheckSourceDestination(u.setID, u.folder, u.dest); err != nil {
		return "", "", err
	}
	rel := path.Join(u.folder, name)
	full := path.Join(u.r.d.RemotePath, rel)
	verified, err := u.sess.Upload(ctx, staged.Path, rel, staged.Digest)
	if err != nil {
		return "", "", u.b.sourceWriteError(ctx, u.r, full, err)
	}
	return full, string(verified), nil
}

func (u *sourceUpload) close() {
	if u != nil && u.sess != nil {
		_ = u.sess.Close()
	}
}

// sourceWriteError words a transport failure writing onto the source host.
// full is the file's path, empty for the session and the folder.
func (b *BackupService) sourceWriteError(ctx context.Context, r resolvedSourceHost, full string, err error) error {
	host := r.d.Host
	var left *transport.UploadNotRemovedError
	leftNote := ""
	if errors.As(err, &left) {
		b.logger.Error(ctx, "recovery_source_upload_not_removed", err)
		leftNote = "; its temporary file " + left.Path + " could not be removed, so delete it on " + host
	}
	switch {
	case errors.Is(err, transport.ErrSourceObjectExists):
		return recoveryErr(RecoveryTargetExists, "%s already holds something at %s; recovery never replaces anything%s", host, full, leftNote)
	case errors.Is(err, transport.ErrSourceDigestMismatch):
		return &RecoveryError{Code: RecoveryFailed, Cause: err,
			Message: fmt.Sprintf("%s's own SHA-256 of %s did not match the recovered copy, so the file was removed again%s", host, full, leftNote)}
	case ctx.Err() != nil:
		return ctx.Err()
	}
	if cat, ok := transport.CategoryOf(err); ok {
		switch cat {
		case transport.PermissionDenied:
			return &RecoveryError{Code: RecoverySourceHostReadOnly, Cause: err,
				Message: fmt.Sprintf("%s refused the upload: the account retnd pulls with (%s) may not write under %s; nothing was left under the file's name%s", host, r.d.User, r.d.RemotePath, leftNote)}
		case transport.Transient, transport.Unclassified:
			b.logger.Error(ctx, "recovery_source_upload", err)
			return &RecoveryError{Code: RecoverySourceHostUnreachable, Cause: err,
				Message: "the connection to " + host + " failed while writing, so nothing was left under the file's name" + leftNote + "; try again, and the engine log has the cause"}
		}
	}
	b.logger.Error(ctx, "recovery_source_upload", err)
	return &RecoveryError{Code: RecoveryFailed, Cause: err, Message: "the upload onto " + host + " failed; nothing was left under the file's name" + leftNote + ", and the engine log has the cause"}
}

// RecoveryGrantWritesToSourceHost says whether redeeming this grant would
// write onto a source host, without redeeming it, so the HTTP layer can put
// the redeem behind the gate again.
func (b *BackupService) RecoveryGrantWritesToSourceHost(token string) bool {
	h := &b.recovery
	if h.grants == nil {
		return false
	}
	kind, ok := h.grants.TargetKindOf(token)
	return ok && kind == export.TargetSource
}
