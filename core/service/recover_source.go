package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/transport"
)

// The "Source host (SFTP)" recovery destination (issue #1191): a recovered
// backup goes back where it came from, an SFTP upload onto the host the set
// pulls from, over the set's own settings.
//
// # Why it is the default and still asks first
//
// Sending a backup home is what an operator recovering one usually wants, so
// every dialog starts on it and `retnd recover` with no --to means it. It is
// also the one recovery that writes onto a production machine, so it is
// never implied: the request carries an explicit acknowledgement (the
// service refuses without one), the HTTP routes put it behind #1163's gate
// and step-up, and nothing already on the source is ever replaced.
//
// # Where the files go
//
// Into a new folder, retnd-recovered-<time>, under the set's remote path.
// Never into the set's own folder next to the files it backs up, and never a
// folder without that prefix, because internal/discovery skips the prefix
// and nothing else: a recovered file anywhere else in the tree would be taken
// for a new backup by the next run, and on a set that deletes from its
// source, deleted.
//
// # Read-only accounts
//
// Pull accounts are often read-only by design. A set declared read_only is
// refused without being touched. For the rest, the preflight runs #852's
// write probe (write one probe file, remove it, confirm it is gone) and says
// what it found before anything runs, and the mint runs it again before a
// byte is read, so a refused upload never costs a read or leaves a staged
// file behind.

// The source-host refusals.
const (
	// RecoverySourceUnavailable: the set has no source host to send a backup
	// back to (not SFTP, an incremental set), or retnd could not reach it to
	// check that it may write there.
	RecoverySourceUnavailable = "RECOVERY_SOURCE_UNAVAILABLE"
	// RecoverySourceReadOnly: the set is declared read_only, or the account
	// it pulls with may not write on the source.
	RecoverySourceReadOnly = "RECOVERY_SOURCE_READ_ONLY"
	// RecoverySourceNotAcknowledged: the request did not say yes to writing
	// onto the source.
	RecoverySourceNotAcknowledged = "RECOVERY_SOURCE_NOT_ACKNOWLEDGED"
)

// The three answers a preflight gives about writing on the source.
const (
	SourceWritableYes     = "yes"
	SourceWritableNo      = "no"
	SourceWritableUnknown = "unknown"
)

// maxRecoveredFolderLen bounds a folder name an operator types.
const maxRecoveredFolderLen = 128

// RecoverySourceDestination is what one backup set's source-host destination
// is, and, after a preflight, whether retnd may write there.
type RecoverySourceDestination struct {
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

	// Folder is the new folder a recovery started now would go into.
	Folder string

	// Writable is "yes", "no" or "unknown" after a preflight, "no" for a set
	// declared read_only, and "" when nobody has asked yet. Detail is the
	// sentence that goes with it.
	Writable string
	Detail   string
}

// URL is the destination as the operator would type it.
func (d RecoverySourceDestination) URL() string {
	if !d.Available {
		return ""
	}
	host := d.Host
	if d.Port != 0 && d.Port != 22 {
		host = fmt.Sprintf("%s:%d", d.Host, d.Port)
	}
	return "sftp://" + d.User + "@" + host + path.Join("/", d.RemotePath, d.Folder) + "/"
}

// defaultRecoveredFolder is a new folder name, unique to the second.
func defaultRecoveredFolder() string {
	return transport.RecoveredFolderPrefix + now().UTC().Format("20060102-150405")
}

// validRecoveredFolder is the rule a folder named by a caller meets: one
// segment, carrying the prefix discovery skips.
func validRecoveredFolder(folder string) bool {
	return len(folder) > len(transport.RecoveredFolderPrefix) && len(folder) <= maxRecoveredFolderLen &&
		strings.HasPrefix(folder, transport.RecoveredFolderPrefix) &&
		!strings.ContainsAny(folder, "/\\\x00") && folder != "." && folder != ".."
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

// RecoverySourceFor describes the set's source-host destination from its
// configuration alone. It never touches the network.
func (b *BackupService) RecoverySourceFor(setID string) RecoverySourceDestination {
	d := RecoverySourceDestination{SetID: setID}
	id, ok := setIDOf(setID)
	if !ok {
		d.Reason = "there is no such backup set"
		return d
	}
	d.SetID = id.String()
	_, bs, found := configuredSource(b.state.Load().inner.Config.Sources, id)
	switch {
	case !found:
		d.Reason = "there is no such backup set in this configuration"
		return d
	case bs.Engine == model.EngineKopia:
		d.Reason = "this is an incremental (kopia) set: its backups are restore points in a repository, not files, so they come back with `" + recoverVerb + " snapshot restore` and there is no source-host upload for it"
		return d
	case bs.Remote.Type != "sftp":
		kind := bs.Remote.Type
		if kind == "local" {
			kind = "local folder"
		}
		d.Reason = "this set's source is a " + kind + ", not SFTP, so there is no source host to send the backup back to; choose another destination"
		return d
	}
	d.Available = true
	d.Host, d.Port, d.User, d.RemotePath = bs.Remote.Host, bs.Remote.Port, bs.Remote.User, bs.RemotePath
	if d.Port == 0 {
		d.Port = 22
	}
	d.Folder = defaultRecoveredFolder()
	if bs.ReadOnly {
		d.Writable = SourceWritableNo
		d.Detail = "this set is declared read_only (pull from here, never change anything here), so retnd will not write onto " + d.Host + "; choose another destination"
	}
	return d
}

// PreflightRecoverySource is RecoverySourceFor plus the answer to "may retnd
// write there", found the only way it can be: by writing a probe file under
// the set's remote path and removing it again (#852). A set with no source
// host, or one declared read_only, is answered without a probe.
func (b *BackupService) PreflightRecoverySource(ctx context.Context, setID string) (RecoverySourceDestination, error) {
	d := b.RecoverySourceFor(setID)
	if !d.Available || d.Writable == SourceWritableNo {
		return d, nil
	}
	err := b.probeSource(ctx, d.SetID)
	switch {
	case err == nil:
		d.Writable = SourceWritableYes
		d.Detail = "retnd can write on " + d.Host + " as " + d.User
	case errors.Is(err, errNoSourceProbe):
		d.Writable = SourceWritableUnknown
		d.Detail = "this deployment cannot check ahead whether it may write on " + d.Host + ", so the upload itself will find out"
	default:
		re, _ := AsRecoveryError(err)
		if re != nil && re.Code == RecoverySourceReadOnly {
			d.Writable = SourceWritableNo
		} else {
			d.Writable = SourceWritableUnknown
		}
		d.Detail = err.Error()
	}
	return d, nil
}

// DefaultRecoveryTarget is the destination a recovery of this artifact or
// set starts on: the source host, in a new folder, unacknowledged. When the
// set has none, the zero target and the reason.
func (b *BackupService) DefaultRecoveryTarget(id string) (RecoveryTarget, string) {
	d := b.RecoverySourceFor(id)
	if !d.Available {
		return RecoveryTarget{}, d.Reason
	}
	return RecoveryTarget{Kind: "source", Path: d.Folder}, ""
}

var errNoSourceProbe = errors.New("service: this transport cannot probe a source for write access")

// probeSource runs the write probe against the set's source and words what
// it found. It writes one probe file and removes it.
func (b *BackupService) probeSource(ctx context.Context, setID string) error {
	st := b.state.Load()
	id, _ := setIDOf(setID)
	src, bs, ok := configuredSource(st.inner.Config.Sources, id)
	if !ok {
		return recoveryErr(RecoverySourceUnavailable, "there is no such backup set in this configuration")
	}
	probe, _ := st.inner.Transport.(transport.SourceWriteProbe)
	if probe == nil {
		return errNoSourceProbe
	}
	err := probe.ProbeSourceWrite(ctx, app.SourceFor(st.inner.Config, src, bs))
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	host := bs.Remote.Host
	if errors.Is(err, transport.ErrProbeNotRemoved) {
		// It could write, it could not tidy up. Writing is what a recovery
		// needs, so this is not a refusal, but the operator has a file of
		// ours to delete and is told so.
		b.logger.Error(ctx, "recovery_source_probe", err)
		return nil
	}
	if cat, ok := transport.CategoryOf(err); ok && cat == transport.PermissionDenied {
		return recoveryErr(RecoverySourceReadOnly,
			"the account retnd pulls with (%s) is read-only on %s, so an upload there will be refused until that account may write under %s; choose another destination, or give it write access",
			bs.Remote.User, host, bs.RemotePath)
	}
	b.logger.Error(ctx, "recovery_source_probe", err)
	return &RecoveryError{Code: RecoverySourceUnavailable,
		Message: "retnd could not reach " + host + " to check that it may write there, so nothing was started; the engine log has the cause", Cause: err}
}

// validateSourceTarget checks a source target against the set it would write
// to, and resolves the folder. name is the file's name for a single
// recovery, empty for a run of several. requireAck is false for a plan,
// which asks what would happen and writes nothing.
func (b *BackupService) validateSourceTarget(setID string, t RecoveryTarget, name string, requireAck bool) (export.Target, string, error) {
	d := b.RecoverySourceFor(setID)
	if !d.Available {
		return export.Target{}, "", recoveryErr(RecoverySourceUnavailable, "%s", d.Reason)
	}
	folder := t.Path
	if folder == "" {
		folder = d.Folder
	}
	if !validRecoveredFolder(folder) {
		return export.Target{}, "", recoveryErr(RecoveryTargetInvalid,
			"a source-host recovery goes into a new folder directly under the set's remote path whose name starts with %s (the next backup run skips those), and %q is not one", transport.RecoveredFolderPrefix, folder)
	}
	if d.Writable == SourceWritableNo {
		return export.Target{}, "", recoveryErr(RecoverySourceReadOnly, "%s", d.Detail)
	}
	if requireAck && !t.Acknowledged {
		d.Folder = folder
		return export.Target{}, "", recoveryErr(RecoverySourceNotAcknowledged,
			"this writes onto %s, the host this backup came from, into %s; say so explicitly (acknowledged in the request, --acknowledge-source-write in the terminal)", d.Host, d.URL())
	}
	return export.Target{Kind: export.TargetSource, Path: path.Join(folder, name)}, folder, nil
}

// uploadToSource writes a verified staged file onto the set's source at rel,
// never replacing anything there.
func (b *BackupService) uploadToSource(ctx context.Context, staged *export.Staged, setID, rel string) (string, error) {
	st := b.state.Load()
	id, _ := setIDOf(setID)
	src, bs, ok := configuredSource(st.inner.Config.Sources, id)
	if !ok {
		return "", recoveryErr(RecoverySourceUnavailable, "this set is no longer in the configuration, so its source is unknown")
	}
	up, _ := st.inner.Transport.(transport.SourceUploader)
	if up == nil {
		return "", recoveryErr(RecoverySourceUnavailable, "this deployment has no way to write onto a source")
	}
	err := up.UploadToSource(ctx, app.SourceFor(st.inner.Config, src, bs), staged.Path, rel)
	full := path.Join(bs.RemotePath, rel)
	switch {
	case err == nil:
		return full, nil
	case errors.Is(err, transport.ErrSourceObjectExists):
		return "", recoveryErr(RecoveryTargetExists, "%s already holds a file at %s; recovery never replaces one", bs.Remote.Host, full)
	}
	if cat, ok := transport.CategoryOf(err); ok && cat == transport.PermissionDenied {
		return "", &RecoveryError{Code: RecoverySourceReadOnly, Cause: err,
			Message: fmt.Sprintf("%s refused the upload: the account retnd pulls with (%s) may not write under %s; nothing was left there", bs.Remote.Host, bs.Remote.User, bs.RemotePath)}
	}
	return "", err
}
