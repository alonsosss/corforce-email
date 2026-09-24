package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// FormRepository guarda los formularios de suscripcion y sus envios validos.
type FormRepository interface {
	// Create: domain.ErrFormExists si el nombre ya esta; domain.ErrListNotFound si la lista no
	// es de la empresa.
	Create(ctx context.Context, f *domain.SubscriptionForm) error
	// Get: domain.ErrFormNotFound si no existe en la empresa.
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.SubscriptionForm, error)
	Update(ctx context.Context, f *domain.SubscriptionForm) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.SubscriptionForm, int64, error)
	// UsingList dice si algun formulario tiene la lista como destino.
	UsingList(ctx context.Context, tenantID, listID uuid.UUID) (bool, error)
	InsertSubmission(ctx context.Context, s *domain.FormSubmissionRecord) error
	// ConfirmSubmission marca confirmado el envio que pidio el token y devuelve su lista
	// destino; nil si el token no vino de un formulario (o su lista ya no existe).
	ConfirmSubmission(ctx context.Context, tenantID, tokenID uuid.UUID, at time.Time) (*uuid.UUID, error)
	// Stats cuenta los envios del formulario creados en [from, to), con la serie por dia UTC.
	Stats(ctx context.Context, tenantID, formID uuid.UUID, from, to time.Time) (*domain.FormStats, error)
}

// SubmissionGuard es el freno anti abuso de los envios publicos, comun a todas las replicas.
// Cada metodo cuenta la operacion y devuelve si cabe y cuanto falta para reintentar.
type SubmissionGuard interface {
	AllowIP(ctx context.Context, ip string) (bool, time.Duration)
	AllowForm(ctx context.Context, tenantID, formID uuid.UUID) (bool, time.Duration)
	// FirstUse dice si el nonce de un token de formulario no se habia usado antes.
	FirstUse(ctx context.Context, nonce string) bool
}
