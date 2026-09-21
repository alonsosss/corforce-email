// Package reconcile adapta el caso de uso de mail-dav al barrido de conciliacion de buzones borrados
// (pkg/mailreconcile): que buzones tienen datos y como retirar los de uno.
package reconcile

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// UseCase es lo que el barrido necesita de mail-dav.
type UseCase interface {
	StaleMailboxes(ctx context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error)
	ReconcileMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int, error)
}

// Store implementa mailreconcile.Store sobre el caso de uso.
type Store struct {
	uc UseCase
}

func NewStore(uc UseCase) *Store { return &Store{uc: uc} }

func (s *Store) StaleMailboxes(ctx context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	return s.uc.StaleMailboxes(ctx, tenantID, before, after, limit)
}

func (s *Store) PurgeMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int, error) {
	return s.uc.ReconcileMailbox(ctx, tenantID, mailboxID)
}
