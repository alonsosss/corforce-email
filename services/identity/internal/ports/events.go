package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
)

// AccountEvents publica por la outbox del registro los hechos del ciclo de vida de una cuenta
// que otro servicio necesita con garantia. Se llama dentro de Transactor.Transact: el evento
// existe si y solo si el cambio se confirma.
type AccountEvents interface {
	// UserDeleted anuncia identity.user.deleted; access-control retira con el las
	// asignaciones de roles de la cuenta.
	UserDeleted(ctx context.Context, d domain.UserDeletion) error
}

// Transactor abre la transaccion de negocio. Los repositorios y el publicador de la outbox
// que reciben el contexto de fn escriben en ella.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

type EventPublisher interface {
	PublishUserCreated(tenantID, userID, email string) error
	// El user-agent viaja junto a la IP para que el detector de seguridad (servicio
	// audit) pueda reconocer inicios desde dispositivos nuevos.
	PublishUserLoggedIn(tenantID, userID, ip, userAgent string) error
	PublishUserLoggedOut(tenantID, userID string) error
	PublishUserLocked(tenantID, userID string) error
	// PublishLoginFailed alimenta la deteccion de fuerza bruta por IP.
	PublishLoginFailed(tenantID, userID, email, ip, userAgent string) error
	// PublishSessionRevoked deja rastro del cierre remoto de sesion por un admin.
	PublishSessionRevoked(tenantID, actorID, targetUserID, sessionID, ip string) error
	PublishPasswordChanged(tenantID, userID string) error
}
