package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

// Authenticator resuelve la contrasena SMTP (el token de la clave de API) contra access-control,
// con una cache corta que respeta la revocacion. Devuelve domain.ErrAuthFailed si no es una clave
// que pueda enviar y domain.ErrAuthUnavailable si no se pudo comprobar.
type Authenticator interface {
	Authenticate(ctx context.Context, token, clientIP string) (*domain.Credential, error)
}

// Throttle es el freno de fuerza bruta: fallos por (usuario, IP) y por IP con bloqueo temporal,
// comun a todas las replicas.
type Throttle interface {
	Blocked(ctx context.Context, username, ip string) bool
	Failure(ctx context.Context, username, ip string)
	Success(ctx context.Context, username, ip string)
}

// Limits cuenta conexiones y mensajes por minuto. Devuelve si la operacion cabe en el cupo.
type Limits interface {
	AllowConnection(ctx context.Context, ip string) bool
	AllowMessage(ctx context.Context, keyID, ip string) bool
}

// Inspector lee el MIME al terminar el DATA. domain.ErrMalformed o domain.ErrTooLarge si no se
// admite.
type Inspector interface {
	Inspect(raw []byte) (domain.Inspection, error)
}

// Scanner analiza el mensaje con ClamAV. domain.ErrInfected o domain.ErrScanUnavailable.
type Scanner interface {
	Scan(ctx context.Context, raw []byte) error
}

// Submitter entrega el mensaje a transactional. Devuelve el id del mensaje o un
// *domain.Rejection con la clase que corresponde a la respuesta.
type Submitter interface {
	Submit(ctx context.Context, cred domain.Credential, env domain.Envelope, raw []byte, idempotencyKey string) (string, error)
}

// Metrics cuenta lo que pasa en las sesiones. Etiquetas de conjuntos cerrados.
type Metrics interface {
	Connection(listener string)
	Auth(result string)
	Rejected(stage, reason string)
	Delivered(size int, took time.Duration)
}
