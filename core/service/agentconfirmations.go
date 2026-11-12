package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// Confirmations: the human in the loop (EPIC U, FR-95; ADR 0034 decision 5).
//
// A tool call under operate or configure never executes. It becomes a pending
// confirmation: the request the call would have made, kept byte for byte, with
// the echoed command, the set, the token and an expiry. A person approves it
// (with a step-up, in apps/common/webhost) or denies it, and approval replays
// the record, never anything the approver or the agent sends afterwards.
//
// This file owns the record and its states. It does not own the replay: that
// has to go through the handler the browser would have hit, which is the web
// host's, so ApproveAgentConfirmation only claims the confirmation and hands
// the record back, and FinishAgentApproval writes down how the replay ended.
//
// # Where the digest lives
//
// agent_confirmations has no column for a digest and the epic adds no second
// migration, so the SHA-256 of the body is written, in the same transaction as
// the row, as the first agent_audit row naming the confirmation (transport
// "confirmation"). Approval recomputes it from the stored body and refuses a
// body that no longer matches, so a record changed between creation and
// approval is never replayed. The same table keeps one row per state change
// and, under transport "replay", the execution's own result, which is what
// confirmation_status reads back.

const (
	// DefaultAgentConfirmationTTL is how long a confirmation waits for a
	// person (agents.confirmation_ttl).
	DefaultAgentConfirmationTTL = 15 * time.Minute
	// DefaultMaxPendingAgentConfirmations is how many a token may have open
	// at once (agents.max_pending_confirmations).
	DefaultMaxPendingAgentConfirmations = 5

	maxAgentConfirmationBody = 64 << 10
	maxAgentExecutionMessage = 512
	agentConfirmationListMax = 200

	// The audit transports this file writes.
	auditTransportConfirmation = "confirmation"
	auditTransportReplay       = "replay"

	// The exit codes the CLI gives a refusal from the engine (core/cmd/retnd
	// engineRefusalExit), spelled here so a tool result can carry the one the
	// CLI would have used. A test over there holds the two equal.
	agentExitGateShut          = 50
	agentExitInsecureTransport = 52
)

var (
	// ErrAgentConfirmationLimit: the token already has the most open
	// confirmations it may have (AGENT_CONFIRMATION_LIMIT).
	ErrAgentConfirmationLimit = errors.New("service: the token has too many pending confirmations")
	// ErrAgentConfirmationExpired: the confirmation's time passed before
	// anyone decided it (AGENT_CONFIRMATION_EXPIRED).
	ErrAgentConfirmationExpired = errors.New("service: the confirmation has expired")
	// ErrAgentConfirmationNotFound: no such confirmation, or not this token's.
	ErrAgentConfirmationNotFound = errors.New("service: no such confirmation")
	// ErrAgentConfirmationNotPending: it was already approved or denied.
	ErrAgentConfirmationNotPending = errors.New("service: the confirmation was already decided")
	// ErrAgentConfirmationBodySubstituted: the stored request no longer
	// matches the digest taken when it was recorded
	// (AGENT_CONFIRMATION_BODY_SUBSTITUTED).
	ErrAgentConfirmationBodySubstituted = errors.New("service: the recorded request changed after it was recorded")
	// ErrAgentConfirmationTokenEnded: the token that asked was revoked or has
	// expired, so approving would run something nobody can be held to.
	ErrAgentConfirmationTokenEnded = errors.New("service: the token that asked for this has been revoked or has expired")
	// ErrAgentScopeInsufficient: the token's scope does not cover the tool.
	ErrAgentScopeInsufficient = errors.New("service: the token's scope does not cover this tool")
)

// AgentExecution is how an approved confirmation's replay ended: the status
// and error code the API answered with, its message, the correlation id of the
// request and the exit code the CLI would have used for the same answer.
type AgentExecution struct {
	Status        int
	Code          string
	Message       string
	CorrelationID string
	ExitCode      int
}

// AgentConfirmation is a confirmation as every surface shows it. Body is the
// recorded request and is only ever read to replay it; Contract leaves it out.
type AgentConfirmation struct {
	ID        string
	TokenID   string
	TokenName string
	Tool      string
	Route     string
	Method    string
	Command   string
	BackupSet string
	State     string
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt *time.Time
	DecidedBy string
	Body      []byte
	// Execution is set once an approved confirmation's replay has finished.
	Execution *AgentExecution
}

// Contract is the confirmation as /api/v1 shows it, without the body.
func (c AgentConfirmation) Contract() apicontract.AgentConfirmation {
	out := apicontract.AgentConfirmation{
		ID: c.ID, TokenID: c.TokenID, TokenName: c.TokenName, Tool: c.Tool, Route: c.Route, Method: c.Method,
		Command: c.Command, BackupSet: c.BackupSet, State: c.State,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: c.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if c.DecidedAt != nil {
		s := c.DecidedAt.UTC().Format(time.RFC3339)
		out.DecidedAt = &s
	}
	if c.DecidedBy != "" {
		out.DecidedBy = &c.DecidedBy
	}
	if e := c.Execution; e != nil {
		out.Execution = &apicontract.AgentExecution{
			Status: e.Status, Code: e.Code, Message: e.Message, CorrelationID: e.CorrelationID, ExitCode: e.ExitCode,
		}
	}
	return out
}

// CreateAgentConfirmationRequest is the request a tool call would have made.
// Scope is the scope the catalogue gives the tool; Command is the cliecho echo
// of the request; BackupSet is the id of the set it concerns, or empty.
type CreateAgentConfirmationRequest struct {
	Principal AgentPrincipal
	Scope     string
	Tool      string
	Method    string
	Route     string
	Body      []byte
	Command   string
	BackupSet string
}

// SetAgentConfirmationLimits sets agents.confirmation_ttl and
// agents.max_pending_confirmations. It is called once while the engine is
// wired, before anything is served; zero keeps the default.
func (b *BackupService) SetAgentConfirmationLimits(ttl time.Duration, maxPending int) {
	b.agentConfirmationTTL, b.agentMaxPending = ttl, maxPending
}

func (b *BackupService) agentConfirmationLimits() (time.Duration, int) {
	ttl, maxPending := b.agentConfirmationTTL, b.agentMaxPending
	if ttl <= 0 {
		ttl = DefaultAgentConfirmationTTL
	}
	if maxPending <= 0 {
		maxPending = DefaultMaxPendingAgentConfirmations
	}
	return ttl, maxPending
}

// CreateAgentConfirmation records a tool call as a pending confirmation and
// executes nothing. The token is read from its row again, so a token revoked a
// moment ago creates nothing, and its scope has to cover the tool.
func (b *BackupService) CreateAgentConfirmation(ctx context.Context, req CreateAgentConfirmationRequest) (AgentConfirmation, error) {
	switch {
	case req.Tool == "" || req.Route == "" || req.Command == "":
		return AgentConfirmation{}, fmt.Errorf("%w: a confirmation records a tool, a route and the command it echoes", ErrInvalidRequest)
	case req.Method != "POST" && req.Method != "PUT" && req.Method != "PATCH" && req.Method != "DELETE":
		return AgentConfirmation{}, fmt.Errorf("%w: only a request that changes something is confirmed, not %q", ErrInvalidRequest, req.Method)
	case !strings.HasPrefix(req.Route, "/"):
		return AgentConfirmation{}, fmt.Errorf("%w: the route %q is not a path below /api/v1", ErrInvalidRequest, req.Route)
	case len(req.Body) > maxAgentConfirmationBody:
		return AgentConfirmation{}, fmt.Errorf("%w: the request is %d bytes, over the %d a confirmation records", ErrInvalidRequest, len(req.Body), maxAgentConfirmationBody)
	}
	b.sweepAgentConfirmations(ctx)

	tok, err := b.journal.GetAgentToken(ctx, req.Principal.TokenID)
	if errors.Is(err, state.ErrAgentTokenNotFound) {
		return AgentConfirmation{}, ErrAgentTokenInvalid
	}
	if err != nil {
		return AgentConfirmation{}, fmt.Errorf("service: create agent confirmation: %w", err)
	}
	at := now()
	if tok.RevokedAt != nil || !at.Before(tok.ExpiresAt) {
		return AgentConfirmation{}, ErrAgentTokenInvalid
	}
	// configure is the widest scope and covers operate; read covers neither.
	scopes := []string{AgentScopeRead, AgentScopeOperate, AgentScopeConfigure}
	if need := slices.Index(scopes, req.Scope); need < 1 || slices.Index(scopes, tok.Scope) < need {
		return AgentConfirmation{}, fmt.Errorf("%w: %s needs %s and the token is %s", ErrAgentScopeInsufficient, req.Tool, req.Scope, tok.Scope)
	}

	ttl, maxPending := b.agentConfirmationLimits()
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return AgentConfirmation{}, fmt.Errorf("service: create agent confirmation: %w", err)
	}
	row := state.AgentConfirmation{
		ID: "cnf_" + hex.EncodeToString(idBytes), TokenID: tok.ID, Tool: req.Tool, Route: req.Route, Method: req.Method,
		Body: slices.Clone(req.Body), Command: req.Command, BackupSet: req.BackupSet,
		State: state.AgentConfirmationPending, CreatedAt: at, ExpiresAt: at.Add(ttl),
	}
	record := state.AgentAudit{
		At: at, TokenID: tok.ID, Tool: req.Tool, Transport: auditTransportConfirmation,
		Arguments: "sha256:" + bodyDigest(req.Body), Status: 409, Code: "AGENT_CONFIRMATION_REQUIRED", ConfirmationID: row.ID,
	}
	if err := b.journal.InsertAgentConfirmation(ctx, row, maxPending, record); err != nil {
		if errors.Is(err, state.ErrAgentConfirmationLimit) {
			return AgentConfirmation{}, fmt.Errorf("%w: %s already has %d waiting for a person; approve or deny one first", ErrAgentConfirmationLimit, tok.Name, maxPending)
		}
		return AgentConfirmation{}, fmt.Errorf("service: create agent confirmation: %w", err)
	}
	out := confirmationView(row, tok.Name, nil)
	b.emitAgentConfirmation(ctx, out, "agent:"+tok.Name, obs.ResultInfo)
	return out, nil
}

// ListAgentConfirmations returns the newest confirmations first, after
// expiring the ones whose time has passed.
func (b *BackupService) ListAgentConfirmations(ctx context.Context) ([]AgentConfirmation, error) {
	b.sweepAgentConfirmations(ctx)
	rows, err := b.journal.ListAgentConfirmations(ctx, agentConfirmationListMax)
	if err != nil {
		return nil, fmt.Errorf("service: list agent confirmations: %w", err)
	}
	names, err := b.agentTokenNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgentConfirmation, 0, len(rows))
	for _, row := range rows {
		exec, err := b.agentExecution(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, confirmationView(row, names[row.TokenID], exec))
	}
	return out, nil
}

// AgentConfirmationStatus is what the confirmation_status tool reads: the
// state of one confirmation and, once an approval has executed, its result. A
// token reads only its own; another token's id answers not found, so ids
// cannot be probed.
func (b *BackupService) AgentConfirmationStatus(ctx context.Context, tokenID, id string) (AgentConfirmation, error) {
	b.sweepAgentConfirmations(ctx)
	row, err := b.journal.GetAgentConfirmation(ctx, id)
	if errors.Is(err, state.ErrAgentConfirmationNotFound) || (err == nil && row.TokenID != tokenID) {
		return AgentConfirmation{}, fmt.Errorf("%w: %q", ErrAgentConfirmationNotFound, id)
	}
	if err != nil {
		return AgentConfirmation{}, fmt.Errorf("service: agent confirmation status: %w", err)
	}
	names, err := b.agentTokenNames(ctx)
	if err != nil {
		return AgentConfirmation{}, err
	}
	exec, err := b.agentExecution(ctx, row)
	if err != nil {
		return AgentConfirmation{}, err
	}
	return confirmationView(row, names[row.TokenID], exec), nil
}

// ApproveAgentConfirmation claims a pending confirmation for approval and
// hands back the record to replay, with the principal that asked. It checks,
// in this order and changing nothing until the last step: that the
// confirmation exists and is pending, that its time has not passed, that the
// stored body still has the digest it was recorded with, and that the token
// that asked is still live. Then one conditional update makes it approved, so
// two approvals cannot both replay it.
//
// The caller has already checked the step-up. It must replay the returned body
// unchanged and then call FinishAgentApproval.
func (b *BackupService) ApproveAgentConfirmation(ctx context.Context, id, approver string) (AgentConfirmation, AgentPrincipal, error) {
	b.sweepAgentConfirmations(ctx)
	row, err := b.pendingConfirmation(ctx, id)
	if err != nil {
		return AgentConfirmation{}, AgentPrincipal{}, err
	}

	rows, err := b.journal.AgentAuditForConfirmation(ctx, id)
	if err != nil {
		return AgentConfirmation{}, AgentPrincipal{}, fmt.Errorf("service: approve agent confirmation: %w", err)
	}
	recorded := ""
	for _, r := range rows {
		if r.Transport == auditTransportConfirmation && strings.HasPrefix(r.Arguments, "sha256:") {
			recorded = strings.TrimPrefix(r.Arguments, "sha256:")
			break
		}
	}
	// No recorded digest is a mismatch: a record nobody can vouch for is not replayed.
	if subtle.ConstantTimeCompare([]byte(recorded), []byte(bodyDigest(row.Body))) != 1 {
		return AgentConfirmation{}, AgentPrincipal{}, fmt.Errorf("%w: nothing was run; deny it and ask for it again", ErrAgentConfirmationBodySubstituted)
	}

	tok, err := b.journal.GetAgentToken(ctx, row.TokenID)
	if err != nil {
		return AgentConfirmation{}, AgentPrincipal{}, fmt.Errorf("service: approve agent confirmation: %w", err)
	}
	at := now()
	if tok.RevokedAt != nil || !at.Before(tok.ExpiresAt) {
		return AgentConfirmation{}, AgentPrincipal{}, fmt.Errorf("%w: nothing was run; deny it", ErrAgentConfirmationTokenEnded)
	}

	won, err := b.journal.DecideAgentConfirmation(ctx, id, state.AgentConfirmationApproved, approver, at)
	if err != nil {
		return AgentConfirmation{}, AgentPrincipal{}, fmt.Errorf("service: approve agent confirmation: %w", err)
	}
	if !won {
		return AgentConfirmation{}, AgentPrincipal{}, fmt.Errorf("%w: someone decided it first", ErrAgentConfirmationNotPending)
	}
	row.State, row.DecidedBy, row.DecidedAt = state.AgentConfirmationApproved, approver, &at
	b.auditAgentDecision(ctx, row, "approved by "+approver)
	return confirmationView(row, tok.Name, nil), AgentPrincipal{TokenID: tok.ID, Name: tok.Name, Scope: tok.Scope}, nil
}

// FinishAgentApproval records how an approved confirmation's replay ended and
// puts the approval on the feed, with a result that follows the replay's: a
// replay that was refused is a warning, one that failed is an error. The
// approval itself already stands; a replay that was refused by the gate does
// not make it pending again.
func (b *BackupService) FinishAgentApproval(ctx context.Context, c AgentConfirmation, approver string, exec AgentExecution) error {
	exec.ExitCode = AgentExecutionExitCode(exec.Status, exec.Code)
	message := exec.Message
	if len(message) > maxAgentExecutionMessage {
		message = strings.ToValidUTF8(message[:maxAgentExecutionMessage], "") + "..."
	}
	err := b.journal.InsertAgentAudit(ctx, state.AgentAudit{
		At: now(), TokenID: c.TokenID, Tool: c.Tool, Transport: auditTransportReplay, Arguments: message,
		Status: exec.Status, Code: exec.Code, CorrelationID: exec.CorrelationID, ConfirmationID: c.ID,
	})
	if err != nil {
		return fmt.Errorf("service: record agent execution: %w", err)
	}
	result := obs.ResultSuccess
	switch {
	case exec.Status >= 500:
		result = obs.ResultError
	case exec.Status >= 400:
		result = obs.ResultWarn
	}
	c.Execution = &exec
	b.emitAgentConfirmation(ctx, c, approver, result)
	return nil
}

// DenyAgentConfirmation ends a pending confirmation without running it.
func (b *BackupService) DenyAgentConfirmation(ctx context.Context, id, by string) (AgentConfirmation, error) {
	b.sweepAgentConfirmations(ctx)
	row, err := b.pendingConfirmation(ctx, id)
	if err != nil {
		return AgentConfirmation{}, err
	}
	at := now()
	won, err := b.journal.DecideAgentConfirmation(ctx, id, state.AgentConfirmationDenied, by, at)
	if err != nil {
		return AgentConfirmation{}, fmt.Errorf("service: deny agent confirmation: %w", err)
	}
	if !won {
		return AgentConfirmation{}, fmt.Errorf("%w: someone decided it first", ErrAgentConfirmationNotPending)
	}
	row.State, row.DecidedBy, row.DecidedAt = state.AgentConfirmationDenied, by, &at
	names, err := b.agentTokenNames(ctx)
	if err != nil {
		return AgentConfirmation{}, err
	}
	out := confirmationView(row, names[row.TokenID], nil)
	b.auditAgentDecision(ctx, row, "denied by "+by)
	b.emitAgentConfirmation(ctx, out, by, obs.ResultInfo)
	return out, nil
}

// pendingConfirmation reads a confirmation that can still be decided, or says
// why it cannot.
func (b *BackupService) pendingConfirmation(ctx context.Context, id string) (state.AgentConfirmation, error) {
	row, err := b.journal.GetAgentConfirmation(ctx, id)
	if errors.Is(err, state.ErrAgentConfirmationNotFound) {
		return state.AgentConfirmation{}, fmt.Errorf("%w: %q", ErrAgentConfirmationNotFound, id)
	}
	if err != nil {
		return state.AgentConfirmation{}, fmt.Errorf("service: read agent confirmation: %w", err)
	}
	switch row.State {
	case state.AgentConfirmationPending:
		return row, nil
	case state.AgentConfirmationExpired:
		return state.AgentConfirmation{}, fmt.Errorf("%w: it was not decided before %s", ErrAgentConfirmationExpired, row.ExpiresAt.UTC().Format(time.RFC3339))
	default:
		return state.AgentConfirmation{}, fmt.Errorf("%w: it is %s", ErrAgentConfirmationNotPending, row.State)
	}
}

// sweepAgentConfirmations expires every confirmation whose time has passed and
// says so once on the feed. It runs at the start of every read and decision,
// so an expiry is noticed when somebody looks and nothing needs a timer. A
// failure is logged and not returned: the caller's own read or decision is
// what the person asked for.
func (b *BackupService) sweepAgentConfirmations(ctx context.Context) {
	expired, err := b.journal.ExpireAgentConfirmations(ctx, now())
	if err != nil {
		slog.WarnContext(ctx, "agent confirmations were not expired", "error", err)
		return
	}
	if len(expired) == 0 {
		return
	}
	names, err := b.agentTokenNames(ctx)
	if err != nil {
		slog.WarnContext(ctx, "expired confirmations were not reported", "error", err)
		return
	}
	for _, row := range expired {
		b.auditAgentDecision(ctx, row, "expired")
		b.emitAgentConfirmation(ctx, confirmationView(row, names[row.TokenID], nil), "system", obs.ResultWarn)
	}
}

// auditAgentDecision writes the audit row for a state change. The decision
// stands whether or not the row lands, so a failure is logged.
func (b *BackupService) auditAgentDecision(ctx context.Context, row state.AgentConfirmation, what string) {
	err := b.journal.InsertAgentAudit(ctx, state.AgentAudit{
		At: now(), TokenID: row.TokenID, Tool: row.Tool, Transport: auditTransportConfirmation,
		Arguments: what, Status: 200, ConfirmationID: row.ID,
	})
	if err != nil {
		slog.WarnContext(ctx, "an agent confirmation decision was not audited", "confirmation", row.ID, "error", err)
	}
}

// emitAgentConfirmation puts a state change on the feed as agent_action, with
// a result, on the strip of the set it concerns.
func (b *BackupService) emitAgentConfirmation(ctx context.Context, c AgentConfirmation, actor string, result obs.Result) {
	attrs := []slog.Attr{
		slog.String("actor", actor),
		slog.String("token", c.TokenName),
		slog.String("tool", c.Tool),
		slog.String("state", c.State),
		slog.String("confirmation_id", c.ID),
		slog.String("route", c.Method+" /api/v1"+c.Route),
		slog.String("command", c.Command),
		slog.String("expires_at", c.ExpiresAt.UTC().Format(time.RFC3339)),
	}
	if c.DecidedBy != "" {
		attrs = append(attrs, slog.String("decided_by", c.DecidedBy))
	}
	// Only a set this deployment configures, for the reason RecordAPIAction
	// gives: the id is whatever the request said.
	if c.BackupSet != "" && b.configuresBackupSetID(c.BackupSet) {
		attrs = append(attrs, slog.String("backup_set", c.BackupSet))
	}
	if e := c.Execution; e != nil {
		attrs = append(attrs, slog.Int("status", e.Status), slog.Int("exit_code", e.ExitCode))
		if e.Code != "" {
			attrs = append(attrs, slog.String("error_code", e.Code))
		}
		if e.CorrelationID != "" {
			attrs = append(attrs, slog.String("correlation_id", e.CorrelationID))
		}
	}
	b.logger.Completed(ctx, result, obs.EventAgentAction, "confirmation "+c.State+": "+c.Tool, attrs...)
}

// agentExecution reads the result the replay of an approved confirmation
// recorded, or nil when there is none (not approved, or the process stopped
// between the approval and the replay's end).
func (b *BackupService) agentExecution(ctx context.Context, row state.AgentConfirmation) (*AgentExecution, error) {
	if row.State != state.AgentConfirmationApproved {
		return nil, nil
	}
	rows, err := b.journal.AgentAuditForConfirmation(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("service: read agent execution: %w", err)
	}
	for _, r := range slices.Backward(rows) {
		if r.Transport == auditTransportReplay {
			return &AgentExecution{
				Status: r.Status, Code: r.Code, Message: r.Arguments, CorrelationID: r.CorrelationID,
				ExitCode: AgentExecutionExitCode(r.Status, r.Code),
			}, nil
		}
	}
	return nil, nil
}

func (b *BackupService) agentTokenNames(ctx context.Context) (map[string]string, error) {
	tokens, err := b.journal.ListAgentTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: read agent token names: %w", err)
	}
	names := make(map[string]string, len(tokens))
	for _, t := range tokens {
		names[t.ID] = t.Name
	}
	return names, nil
}

func confirmationView(row state.AgentConfirmation, tokenName string, exec *AgentExecution) AgentConfirmation {
	return AgentConfirmation{
		ID: row.ID, TokenID: row.TokenID, TokenName: tokenName, Tool: row.Tool, Route: row.Route, Method: row.Method,
		Command: row.Command, BackupSet: row.BackupSet, State: row.State, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy, Body: row.Body, Execution: exec,
	}
}

func bodyDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// AgentExecutionExitCode is the exit code the CLI gives an answer from the
// engine: 0 for a success, 50 for the shut destructive gate, 52 for plain
// http without the acknowledgement and 1 for every other refusal. It is the
// one place the service says it, and core/cmd/retnd holds it equal to
// engineRefusalExit.
func AgentExecutionExitCode(status int, code string) int {
	switch {
	case code == string(apicontract.ErrorCodeDestructiveOperationsDisabled):
		return agentExitGateShut
	case code == string(apicontract.ErrorCodeInsecureTransportAckRequired):
		return agentExitInsecureTransport
	case status >= 400:
		return 1
	default:
		return 0
	}
}
