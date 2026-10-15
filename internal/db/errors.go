package db

import "errors"

var (
	ErrNotFound        = errors.New("database record not found")
	ErrLastActiveAdmin = errors.New("team must retain at least one active admin")
	// ErrCommitOutcomeUnknown means a transaction's COMMIT acknowledgement was
	// lost. Callers that would perform destructive compensation must reconcile
	// an idempotency/publication key on a fresh connection first.
	ErrCommitOutcomeUnknown = errors.New("database transaction commit outcome is unknown")
	// ErrNoManagedWorkspaceBinding: the bot has no binding to a managed
	// workspace, so "native" cannot be made its primary target.
	ErrNoManagedWorkspaceBinding = errors.New("bot has no managed workspace binding")
)
