package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

// Authenticator verifica una credencial de buzon contra mail-auth (service "dav"). Todo rechazo es
// domain.ErrInvalidCredentials, sin distinguir la causa; un fallo del verificador es
// domain.ErrUnavailable.
type Authenticator interface {
	Authenticate(ctx context.Context, username, password, remoteIP string) (domain.Principal, error)
}

// TenantBinder deja en el contexto lo que hace falta para trabajar en nombre del buzon: la base de su
// empresa y su identidad, que es lo que leen las politicas de fila.
type TenantBinder interface {
	Bind(ctx context.Context, p domain.Principal) (context.Context, error)
}

// Store es el almacen de libretas y contactos. Toda operacion se acota al buzon del Principal: un
// recurso de otro buzon o de otra empresa no existe para quien pregunta (domain.ErrNotFound).
type Store interface {
	ListAddressbooks(ctx context.Context, p domain.Principal) ([]domain.Addressbook, error)
	GetAddressbook(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, error)
	// CreateAddressbook devuelve domain.ErrAlreadyExists si el buzon ya tiene una con ese slug y
	// domain.ErrAddressbookLimit si alcanzo maxBooks.
	CreateAddressbook(ctx context.Context, p domain.Principal, book domain.Addressbook, maxBooks int) (domain.Addressbook, error)
	// DeleteAddressbook borra la libreta con sus contactos.
	DeleteAddressbook(ctx context.Context, p domain.Principal, slug string) error

	// ListContacts devuelve la libreta y todos sus contactos; la libreta se lee primero, de modo que
	// su ctag nunca es posterior a lo listado.
	ListContacts(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, []domain.Contact, error)
	GetContact(ctx context.Context, p domain.Principal, slug, resource string) (domain.Contact, error)
	// GetContacts devuelve solo los recursos que existen, en cualquier orden.
	GetContacts(ctx context.Context, p domain.Principal, slug string, resources []string) ([]domain.Contact, error)
	// PutContact crea o reemplaza el contacto de c.ResourceName. cond se evalua contra el etag actual
	// con la libreta bloqueada. Errores: domain.ErrPreconditionFailed, *domain.UIDConflictError,
	// domain.ErrContactLimit y domain.ErrNotFound (libreta inexistente).
	PutContact(ctx context.Context, p domain.Principal, slug string, c domain.Contact, cond domain.Precondition, maxContacts, maxChanges int) (created bool, err error)
	DeleteContact(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error
	// ChangesSince devuelve, ademas de la libreta, los contactos que existen y cambiaron despues de
	// seq y los nombres de los borrados. domain.ErrInvalidSyncToken si seq ya no se puede resolver.
	ChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64) (book domain.Addressbook, changed []domain.Contact, removed []string, err error)
}
