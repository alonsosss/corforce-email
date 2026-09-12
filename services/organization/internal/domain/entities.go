package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Estados de un tenant. El registro solo enruta a los activos; un tenant suspendido
// conserva su base pero no recibe trafico, y uno inactivo esta dado de baja a la
// espera de que se decida sobre sus datos.
const (
	TenantStatusActive    = "active"
	TenantStatusSuspended = "suspended"
	TenantStatusInactive  = "inactive"
)

// TenantStatuses enumera los estados admitidos, en el mismo orden que el CHECK de la
// tabla. Sirve para validar la entrada sin repetir la lista en cada capa.
func TenantStatuses() []string {
	return []string{TenantStatusActive, TenantStatusSuspended, TenantStatusInactive}
}

// Tenant es una empresa cliente de la plataforma: una base fisica propia, alojada en
// una celda, y un juego de modulos habilitados.
type Tenant struct {
	ID     uuid.UUID
	Slug   string
	Name   string
	DBName string
	Status string
	// CellID es la celda donde vive la base del tenant. Todo tenant nace en una celda;
	// la de la peticion de alta o la celda por defecto de la plataforma.
	CellID    uuid.UUID
	Settings  map[string]interface{}
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsActive indica si el tenant recibe trafico y entra en los barridos de la plataforma.
func (t Tenant) IsActive() bool { return t.Status == TenantStatusActive }

// tenantDBPrefix es el prefijo de las bases fisicas de tenant. El nombre completo se
// guarda en el registro: nadie fuera de este servicio tiene que reconstruirlo.
const tenantDBPrefix = "mail_tenant_"

// TenantDBName deriva el nombre de la base fisica de un tenant a partir de su slug.
// El guion no es valido en un identificador de Postgres sin comillas, asi que se
// sustituye por guion bajo.
func TenantDBName(slug string) string {
	return tenantDBPrefix + strings.ReplaceAll(slug, "-", "_")
}

// Estados de una celda. Solo una celda activa recibe tenants nuevos; una en drenado
// conserva los que tiene mientras se mueven a otra, y una cerrada ya no aloja ninguno.
const (
	CellStatusActive   = "active"
	CellStatusDraining = "draining"
	CellStatusClosed   = "closed"
)

// CellStatuses enumera los estados admitidos de una celda, en el orden del CHECK.
func CellStatuses() []string {
	return []string{CellStatusActive, CellStatusDraining, CellStatusClosed}
}

// Cell es una entrada del directorio de celdas: un Postgres por region donde viven las
// bases de un conjunto de tenants. El directorio describe donde esta cada celda; el
// enrutado de conexiones por celda lo resuelve la capa de acceso a datos.
type Cell struct {
	ID        uuid.UUID
	Code      string
	Region    string
	Status    string
	DBHost    string
	DBPort    int
	CreatedAt time.Time
}

// AcceptsTenants indica si la celda puede recibir un tenant nuevo.
func (c Cell) AcceptsTenants() bool { return c.Status == CellStatusActive }

// TenantMigrationStatus es el estado de las migraciones canonicas sobre la base de un
// tenant. Baselined indica que la base se marco como migrada sin ejecutar el SQL (base
// preexistente): su contenido debe verificarse a mano.
type TenantMigrationStatus struct {
	Applied   int
	Pending   []string
	Baselined bool
}
