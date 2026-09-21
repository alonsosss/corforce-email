package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

// MailboxRepository es lo que la verificacion lee y escribe del directorio de correo.
//
// Las lecturas de FindByUsername y ListAppPasswords no llevan empresa: Dovecot no la
// conoce y username es clave unica global en la celda. Es la unica excepcion de la
// plataforma a leer mail.* sin acotar por tenant, y por eso este puerto no expone nada
// mas del esquema.
type MailboxRepository interface {
	// FindByUsername devuelve el buzon con ese username (ya en minusculas) en cualquier
	// estado; domain.ErrNotFound si no existe.
	FindByUsername(ctx context.Context, username string) (*domain.Mailbox, error)
	// ListAppPasswords devuelve las contrasenas de aplicacion activas del buzon con el
	// flag del protocolo encendido, las mas antiguas primero y con un tope: cada una
	// cuesta una comparacion de bcrypt en cada intento fallido.
	ListAppPasswords(ctx context.Context, mailboxID uuid.UUID, p domain.Protocol) ([]domain.AppPassword, error)
	// TouchAppPassword actualiza last_used_at de la contrasena de aplicacion usada.
	TouchAppPassword(ctx context.Context, id uuid.UUID) error
	// RecordLogin inserta en mail.sasl_logins.
	RecordLogin(ctx context.Context, login domain.Login) error
	// RecentLogins lista los ultimos inicios de un buzon de una empresa, del mas reciente
	// al mas antiguo. Es la unica lectura acotada por tenant del servicio.
	RecentLogins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.Login, error)
}

// PasswordVerifier compara una contrasena con su hash. Se abstrae para que el caso de
// uso pueda probarse sin pagar bcrypt y para fijar el hash ficticio en un solo sitio.
type PasswordVerifier interface {
	// Verify devuelve true si la contrasena corresponde al hash.
	Verify(hash, password string) bool
	// DummyHash es un hash real con el mismo coste que los de produccion. Se compara
	// contra el cuando el buzon no existe, para que la respuesta tarde lo mismo.
	DummyHash() string
}

// Throttle es el freno de fuerza bruta por (usuario, IP) y por IP. Es best-effort:
// cuando el almacen no responde debe comportarse como si no hubiera bloqueo.
type Throttle interface {
	// Blocked indica si la pareja o la IP estan bloqueadas.
	Blocked(ctx context.Context, username, ip string) bool
	// Failure anota un intento fallido y bloquea si se supera el maximo.
	Failure(ctx context.Context, username, ip string)
	// Success limpia el contador de la pareja tras un inicio correcto.
	Success(ctx context.Context, username, ip string)
}

// Metrics recibe el desenlace de cada intento.
type Metrics interface {
	Attempt(service string, result domain.Result)
}
