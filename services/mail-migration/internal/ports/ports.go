package ports

import (
	"context"
	"net/netip"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

// Transactor corre fn en una transaccion de la base de la empresa del contexto. Los repositorios
// y el publicador de eventos participan de ella, de modo que un cambio de estado y su evento se
// confirman juntos.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

type Page struct {
	Offset int
	Limit  int
}

type ListFilter struct {
	MailboxID *uuid.UUID
	Status    *domain.Status
}

type ClaimParams struct {
	RunnerID    string
	LeaseID     uuid.UUID
	Now         time.Time
	LeaseUntil  time.Time
	MaxAttempts int
}

type HeartbeatParams struct {
	LeaseID    uuid.UUID
	Phase      domain.Phase
	Progress   domain.Progress
	Now        time.Time
	LeaseUntil time.Time
}

type FinishParams struct {
	LeaseID  uuid.UUID
	Status   domain.Status
	Progress domain.Progress
	Error    *domain.JobError
	Now      time.Time
}

// InsertLimits son los topes que Insert comprueba bajo el cerrojo de la empresa. MaxActive siempre
// se aplica; los demas, en cero, no. Existen porque la migracion hace salir al ejecutor hacia servidores que elige el
// usuario: sin ellos una empresa podria usarla para probar contrasenas ajenas o sondear Internet.
type InsertLimits struct {
	MaxActive int
	// MaxRecent es lo que la empresa puede haber creado desde RecentSince.
	MaxRecent   int
	RecentSince time.Time
	// MaxAuthFailures es cuantos trabajos contra el mismo servidor y usuario de origen pueden haber
	// terminado con las credenciales rechazadas desde AuthFailuresSince.
	MaxAuthFailures   int
	AuthFailuresSince time.Time
}

// JobRepository persiste los trabajos de UNA empresa: el pool y la transaccion llegan en el
// contexto y toda consulta filtra ademas por tenant_id.
type JobRepository interface {
	// Insert toma el cerrojo de la empresa, comprueba los topes de limits y guarda el trabajo. Debe
	// correr dentro de Transact. ErrTenantLimitReached, ErrTenantRateLimited, ErrSourceAuthCooldown
	// y ErrJobAlreadyActive.
	Insert(ctx context.Context, j *domain.Job, limits InsertLimits) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Job, error)
	List(ctx context.Context, tenantID uuid.UUID, f ListFilter, p Page) ([]domain.Job, int64, error)
	CountActive(ctx context.Context, tenantID uuid.UUID) (int, error)
	// RequestCancel cancela al instante un trabajo pendiente y marca la peticion en uno en curso.
	// ErrNotCancellable si ya termino.
	RequestCancel(ctx context.Context, tenantID, id uuid.UUID, at time.Time) (*domain.Job, error)
	// Claim toma el siguiente trabajo reclamable de la empresa (FOR UPDATE SKIP LOCKED) y lo deja en
	// curso con el lease nuevo. Devuelve nil, nil si no hay ninguno.
	Claim(ctx context.Context, tenantID uuid.UUID, p ClaimParams) (*domain.Job, error)
	// Heartbeat extiende el lease y guarda el progreso; informa si se pidio cancelar. ErrLeaseLost si
	// el lease ya no es de quien llama.
	Heartbeat(ctx context.Context, tenantID, id uuid.UUID, p HeartbeatParams) (cancel bool, err error)
	// Finish cierra el trabajo con su estado final y borra la credencial. ErrLeaseLost si el lease no
	// es de quien llama o el trabajo ya termino.
	Finish(ctx context.Context, tenantID, id uuid.UUID, p FinishParams) (*domain.Job, error)
	// ExpireLost cierra los trabajos en curso cuyo lease vencio y ya no pueden reintentarse (agotaron
	// los intentos) o cuya cancelacion estaba pedida, y borra su credencial.
	ExpireLost(ctx context.Context, tenantID uuid.UUID, now time.Time, maxAttempts int) ([]domain.Job, error)
	// DeleteByMailbox borra todos los trabajos del buzon, en cualquier estado, y devuelve lo que
	// borro tal como estaba (el estado que tenia cada uno). Un buzon sin trabajos no es un error.
	DeleteByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) ([]domain.Job, error)
}

// MailboxRef es lo que el directorio de correo sabe de un buzon destino.
type MailboxRef struct {
	ID       uuid.UUID
	Username string
	Active   bool
}

// MailboxDirectory consulta el buzon en la celda de la empresa (mail-directory).
// ErrMailboxNotFound si no existe en la empresa.
type MailboxDirectory interface {
	Lookup(ctx context.Context, tenantID, mailboxID uuid.UUID) (MailboxRef, error)
}

// HostResolver resuelve el servidor de origen para aplicar la regla anti-SSRF.
type HostResolver interface {
	LookupAddrs(ctx context.Context, host string) ([]netip.Addr, error)
}

// Cipher cifra y descifra la credencial de origen (pkg/crypto.KeyRing). Los aad atan el cifrado a la
// empresa y al trabajo (domain.SourcePasswordAAD): copiado a otra fila no se abre.
type Cipher interface {
	EncryptWithAAD(plaintext, aad []byte) ([]byte, error)
	DecryptWithAAD(data, aad []byte) ([]byte, error)
}

// Tenants abre el contexto de la base de una empresa para trabajar fuera de una peticion con sesion:
// el ejecutor no conoce empresas.
type Tenants interface {
	// For devuelve un contexto con el pool de la empresa. domain.ErrTenantUnknown si no existe.
	For(ctx context.Context, tenantID uuid.UUID) (context.Context, error)
	// ForEach recorre las empresas activas en orden hasta que fn devuelve true.
	ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID) (stop bool)) error
}

// EventPublisher encola los eventos de auditoria en la outbox de la empresa. Debe llamarse dentro
// de la transaccion que cambia el trabajo.
type EventPublisher interface {
	Created(ctx context.Context, j *domain.Job) error
	Started(ctx context.Context, j *domain.Job) error
	CancelRequested(ctx context.Context, j *domain.Job, actorID uuid.UUID) error
	// Finished anuncia el estado final del trabajo: completed, failed o cancelled segun j.Status.
	Finished(ctx context.Context, j *domain.Job, actorID uuid.UUID) error
}
