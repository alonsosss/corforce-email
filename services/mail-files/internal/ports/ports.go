// Package ports son los contratos entre los casos de uso de mail-files y su infraestructura.
package ports

import (
	"context"
	"io"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
)

// Repository guarda los metadatos en la base de la empresa. Cada metodo recibe un contexto ya
// ligado a esa base (TenantBinder) y la empresa explicita.
type Repository interface {
	// CreatePending reserva el fichero en estado pending si cabe en la cuota de la politica, con la
	// comprobacion y el alta en la misma transaccion y serializadas por empresa.
	CreatePending(ctx context.Context, f domain.File, policy domain.Policy, now time.Time) error
	// MarkReady pasa un pending a ready y lo devuelve.
	MarkReady(ctx context.Context, tenantID, id uuid.UUID) (domain.File, error)
	// MarkFailed cierra un pending cuya subida al almacen fallo.
	MarkFailed(ctx context.Context, tenantID, id uuid.UUID) error
	ListByMailbox(ctx context.Context, owner domain.Owner, limit int) ([]domain.File, error)
	Usage(ctx context.Context, owner domain.Owner, now time.Time) (domain.Usage, error)
	// Revoke cierra un enlace vigente del buzon; domain.ErrNotFound si no es suyo y
	// domain.ErrNotRevocable si ya no esta vigente.
	Revoke(ctx context.Context, owner domain.Owner, id uuid.UUID, now time.Time) (domain.File, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (domain.File, error)
	// ClaimDownload suma una descarga si el fichero sigue descargable a la hora now, en una sola
	// sentencia; si no, domain.ErrLinkInvalid.
	ClaimDownload(ctx context.Context, tenantID, id uuid.UUID, now time.Time) (domain.File, error)
	MarkObjectDeleted(ctx context.Context, tenantID, id uuid.UUID) error

	// Barrido.
	ExpireDue(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error)
	FailStalePending(ctx context.Context, tenantID uuid.UUID, before time.Time) (int, error)
	// DueForDeletion devuelve hasta limit ficheros cuyo objeto ya se puede borrar: revocados y
	// fallidos, y caducados o agotados hace mas de grace (una descarga en curso no se corta).
	DueForDeletion(ctx context.Context, tenantID uuid.UUID, now time.Time, grace time.Duration, limit int) ([]domain.File, error)
	PurgeHistory(ctx context.Context, tenantID uuid.UUID, before time.Time) (int, error)
}

// TenantBinder liga el contexto a la base de la empresa. Con buzon, la identidad de la sesion de
// base es la del buzon; sin el (descarga publica, barrido), solo la empresa.
type TenantBinder interface {
	Bind(ctx context.Context, tenantID uuid.UUID, mailboxID uuid.UUID) (context.Context, error)
}

// Tenants recorre las empresas activas con el contexto ya ligado a la base de cada una.
type Tenants interface {
	ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID)) error
}

// LeaderLock toma el cerrojo de lider del barrido; ok es false si otra replica lo tiene.
type LeaderLock func(ctx context.Context) (release func(), ok bool)

// ObjectStore guarda el contenido. Open entrega como mucho size bytes y falla con
// domain.ErrNotFound si el objeto ya no esta.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Open(ctx context.Context, key string, size int64) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// Scanner analiza el contenido con ClamAV: nil es limpio, domain.ErrInfected una firma y
// domain.ErrScanUnavailable cualquier otra cosa.
type Scanner interface {
	Scan(ctx context.Context, r io.Reader) error
}

// Spool guarda la subida en un fichero temporal mientras se analiza: nada llega al almacen sin
// veredicto limpio.
type Spool interface {
	Create() (SpoolFile, error)
}

// SpoolFile es una subida en espera. Rewind vuelve al principio para leerla otra vez y Discard la
// borra; se llama siempre.
type SpoolFile interface {
	io.Writer
	Rewind() (io.Reader, error)
	Discard() error
}
