package postgres

import (
	"context"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SessionTurnClockRepo adapts proxy.SessionTurnClock to the SQLC-generated queries.
type SessionTurnClockRepo struct {
	tx sqlc.DBTX
}

// NewSessionTurnClockRepo wires the adapter over a pgx pool or transaction.
func NewSessionTurnClockRepo(tx sqlc.DBTX) *SessionTurnClockRepo {
	return &SessionTurnClockRepo{tx: tx}
}

var _ proxy.SessionTurnClock = (*SessionTurnClockRepo)(nil)

// AdvanceSessionTurnClock records a finished response and returns the one
// recorded before it.
func (r *SessionTurnClockRepo) AdvanceSessionTurnClock(ctx context.Context, advance proxy.SessionTurnClockAdvance) (proxy.SessionTurnClockReading, bool, error) {
	installationID, err := uuid.Parse(advance.InstallationID)
	if err != nil {
		return proxy.SessionTurnClockReading{}, false, err
	}
	row, err := sqlc.New(r.tx).AdvanceSessionTurnClock(ctx, sqlc.AdvanceSessionTurnClockParams{
		InstallationID:  installationID,
		SessionKey:      advance.SessionKey,
		ResponseEndedAt: pgtype.Timestamptz{Time: advance.ResponseEndedAt, Valid: true},
		ServedModel:     advance.ServedModel,
	})
	if err != nil {
		return proxy.SessionTurnClockReading{}, false, err
	}
	if !row.PreviousResponseEndedAt.Valid || row.PreviousServedModel == nil {
		return proxy.SessionTurnClockReading{}, false, nil
	}
	return proxy.SessionTurnClockReading{
		ResponseEndedAt: row.PreviousResponseEndedAt.Time,
		ServedModel:     *row.PreviousServedModel,
	}, true, nil
}

// SweepExpired deletes clocks for sessions idle more than seven days.
func (r *SessionTurnClockRepo) SweepExpired(ctx context.Context) error {
	return sqlc.New(r.tx).SweepStaleSessionTurnClocks(ctx)
}
