package domain

import (
	"time"

	"github.com/google/uuid"
)

// Purpose dice para que usa la empresa el dominio. Corporate recibe y envia por la
// celda (Postfix/Dovecot); sending solo envia transaccional o marketing por SES
// (fase 3); both, ambas cosas.
type Purpose string

const (
	PurposeCorporate Purpose = "corporate"
	PurposeSending   Purpose = "sending"
	PurposeBoth      Purpose = "both"
)

// Purposes devuelve los valores admitidos en orden estable, para validacion y mensajes.
func Purposes() []string {
	return []string{string(PurposeCorporate), string(PurposeSending), string(PurposeBoth)}
}

// IncludesCorporate indica si el dominio recibe correo por la celda: es lo que decide
// si hace falta el MX y si el dominio se activa en el directorio de mail-directory.
func (p Purpose) IncludesCorporate() bool {
	return p == PurposeCorporate || p == PurposeBoth
}

func (p Purpose) Valid() bool {
	switch p {
	case PurposeCorporate, PurposeSending, PurposeBoth:
		return true
	}
	return false
}

// Status es el ciclo de vida de un dominio.
type Status string

const (
	StatusPending  Status = "pending"
	StatusVerified Status = "verified"
	StatusFailed   Status = "failed"
	StatusDisabled Status = "disabled"
)

// DMARCPolicy es la accion que se pide a los receptores para el correo que no supera
// DMARC. Se publica tal cual en el registro _dmarc.
type DMARCPolicy string

const (
	DMARCNone       DMARCPolicy = "none"
	DMARCQuarantine DMARCPolicy = "quarantine"
	DMARCReject     DMARCPolicy = "reject"
)

func DMARCPolicies() []string {
	return []string{string(DMARCNone), string(DMARCQuarantine), string(DMARCReject)}
}

func (p DMARCPolicy) Valid() bool {
	switch p {
	case DMARCNone, DMARCQuarantine, DMARCReject:
		return true
	}
	return false
}

// DKIMKeyBits es el tamano de las claves RSA que se generan. 2048 es el maximo que cabe
// con holgura en un TXT y el minimo que los receptores grandes aceptan hoy.
const DKIMKeyBits = 2048

// Domain es un dominio de correo de la empresa con su material DKIM. Las claves
// privadas viajan cifradas (KeyRing) y nunca se serializan hacia el API.
type Domain struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Domain            string
	Purpose           Purpose
	Status            Status
	VerificationToken string
	VerifiedAt        *time.Time
	LastCheckedAt     *time.Time

	DKIMSelector      string
	DKIMPrivateKeyEnc []byte
	DKIMPublicKey     string
	DKIMKeyBits       int

	// Clave anterior durante la ventana de gracia de una rotacion. Los cuatro campos van
	// juntos: o hay clave anterior completa, o no hay ninguna.
	DKIMPreviousSelector      string
	DKIMPreviousPrivateKeyEnc []byte
	DKIMPreviousPublicKey     string
	DKIMRotatedAt             *time.Time
	// DKIMPreviousSignedAt es la ultima vez que la clave anterior pudo firmar: la gracia se
	// cuenta desde aqui, porque lo que firmo puede seguir en cola.
	DKIMPreviousSignedAt *time.Time
	// DKIMConfirmedAt: cuando una verificacion vio publicado por primera vez el TXT de la clave
	// actual. Nil tras rotar o revocar.
	DKIMConfirmedAt *time.Time
	// DKIMRevocationPending: una revocacion se guardo y la celda aun no confirmo que sus motores
	// solo tienen la clave nueva.
	DKIMRevocationPending bool

	// DirectoryDeactivationPending: el dominio dejo de recibir por la celda y su directorio
	// puede tenerlo aun activo. Se marca antes de llamar a mail-directory y se quita cuando la
	// desactivacion se confirma; el barrido la repite mientras siga marcada.
	DirectoryDeactivationPending bool

	DMARCPolicy DMARCPolicy
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ActiveInDirectory dice si el dominio debe estar activo en el directorio de la celda:
// verificado y con uso corporativo.
func (d *Domain) ActiveInDirectory() bool {
	return d.Status == StatusVerified && d.Purpose.IncludesCorporate()
}

// KeysMayBeInCell dice si las claves DKIM del dominio pueden estar en el Redis de los motores de
// su celda: mail-security solo las acepta de un dominio activo en el directorio, asi que es el
// que debe estarlo o el que dejo de recibir sin que la desactivacion se confirmara.
func (d *Domain) KeysMayBeInCell() bool {
	return d.ActiveInDirectory() || d.DirectoryDeactivationPending
}

// HasPreviousDKIM indica si queda una clave anterior en gracia.
func (d *Domain) HasPreviousDKIM() bool {
	return d.DKIMPreviousSelector != "" && len(d.DKIMPreviousPrivateKeyEnc) > 0 && d.DKIMRotatedAt != nil
}

// PreviousDKIMSigningEnd es la ultima vez que la clave anterior pudo firmar: la rotacion o, si
// despues se siguio firmando con ella, la ultima verificacion que lo hizo.
func (d *Domain) PreviousDKIMSigningEnd() time.Time {
	if !d.HasPreviousDKIM() {
		return time.Time{}
	}
	end := *d.DKIMRotatedAt
	if d.DKIMPreviousSignedAt != nil && d.DKIMPreviousSignedAt.After(end) {
		end = *d.DKIMPreviousSignedAt
	}
	return end
}

// PreviousDKIMRetireAfter es cuando vence la gracia de la clave anterior: su TXT debe seguir
// publicado hasta entonces. Cero si no hay clave anterior.
func (d *Domain) PreviousDKIMRetireAfter(grace time.Duration) time.Time {
	if !d.HasPreviousDKIM() {
		return time.Time{}
	}
	return d.PreviousDKIMSigningEnd().Add(grace)
}

// PreviousDKIMExpired dice si la ventana de gracia de la clave anterior ya vencio.
func (d *Domain) PreviousDKIMExpired(now time.Time, grace time.Duration) bool {
	return d.HasPreviousDKIM() && now.After(d.PreviousDKIMRetireAfter(grace))
}

// ClearPreviousDKIM retira la clave anterior una vez fuera de gracia.
func (d *Domain) ClearPreviousDKIM() {
	d.DKIMPreviousSelector = ""
	d.DKIMPreviousPrivateKeyEnc = nil
	d.DKIMPreviousPublicKey = ""
	d.DKIMRotatedAt = nil
	d.DKIMPreviousSignedAt = nil
}

// DKIMSelectors son los selectores que el dominio custodia: el actual y, en gracia, el anterior.
func (d *Domain) DKIMSelectors() []string {
	if d.HasPreviousDKIM() {
		return []string{d.DKIMSelector, d.DKIMPreviousSelector}
	}
	return []string{d.DKIMSelector}
}

// RecordKind identifica cada registro DNS que se comprueba.
type RecordKind string

const (
	RecordOwnershipTXT RecordKind = "ownership_txt"
	RecordMX           RecordKind = "mx"
	RecordSPF          RecordKind = "spf"
	RecordDKIM         RecordKind = "dkim"
	// RecordDKIMPrevious es el TXT del selector anterior mientras dura la gracia de una
	// rotacion: el cliente no debe retirarlo hasta que venza.
	RecordDKIMPrevious RecordKind = "dkim_previous"
	RecordDMARC        RecordKind = "dmarc"
)

// DNSCheck es el resultado de comprobar un registro en una verificacion concreta.
type DNSCheck struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	DomainID  uuid.UUID
	CheckedAt time.Time
	Record    RecordKind
	Expected  string
	Observed  string
	OK        bool
	Detail    string
}

// DNSRecord es un registro que el cliente debe publicar en su zona.
type DNSRecord struct {
	Record   RecordKind `json:"record"`
	Type     string     `json:"type"`
	Host     string     `json:"host"`
	Value    string     `json:"value"`
	Required bool       `json:"required"`
}

// MXRecord es lo que responde el DNS a una consulta MX, sin depender de net.MX para
// que el dominio no importe infraestructura.
type MXRecord struct {
	Host     string
	Priority uint16
}
