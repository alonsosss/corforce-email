// Package domain son las reglas de los ficheros grandes que un buzon comparte por enlace: estados,
// topes, cuotas, nombres seguros y la firma del enlace publico. Sin infraestructura.
package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Status es el estado guardado de un fichero.
type Status string

const (
	StatusPending Status = "pending"
	StatusReady   Status = "ready"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
	StatusFailed  Status = "failed"
)

// State es lo que ve el remitente: el estado guardado mas lo que se deduce de la hora y de las
// descargas (un fichero listo que caduco o agoto sus descargas ya no se descarga aunque el barrido
// aun no lo haya cerrado).
type State string

const (
	StateUploading State = "uploading"
	StateActive    State = "active"
	StateExpired   State = "expired"
	StateExhausted State = "exhausted"
	StateRevoked   State = "revoked"
	StateFailed    State = "failed"
)

// Owner es el buzon dueno del fichero y su empresa, tal como los verifico mail-auth en la sesion
// del webmail.
type Owner struct {
	TenantID  uuid.UUID
	MailboxID uuid.UUID
}

// File son los metadatos de un fichero compartido. El contenido esta en el almacen, bajo ObjectKey.
type File struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	MailboxID      uuid.UUID
	Name           string
	SizeBytes      int64
	SHA256         string
	ObjectKey      string
	Status         Status
	ExpiresAt      time.Time
	MaxDownloads   int
	Downloads      int
	LastDownloadAt *time.Time
	RevokedAt      *time.Time
	ObjectDeleted  bool
	CreatedAt      time.Time
}

// State deduce lo que ve el remitente a la hora now.
func (f File) State(now time.Time) State {
	switch f.Status {
	case StatusPending:
		return StateUploading
	case StatusRevoked:
		return StateRevoked
	case StatusFailed:
		return StateFailed
	case StatusExpired:
		return StateExpired
	}
	if !now.Before(f.ExpiresAt) {
		return StateExpired
	}
	if f.Downloads >= f.MaxDownloads {
		return StateExhausted
	}
	return StateActive
}

// Downloadable dice si el enlace sirve una descarga a la hora now.
func (f File) Downloadable(now time.Time) bool {
	return f.State(now) == StateActive && !f.ObjectDeleted
}

// RemainingDownloads son las descargas que quedan.
func (f File) RemainingDownloads() int {
	return max(0, f.MaxDownloads-f.Downloads)
}

// ObjectKey es la clave del contenido en el almacen: espacio private/ (el gateway solo sirve
// public/), por empresa y por fichero. Nunca lleva el nombre que dio el usuario.
func ObjectKey(tenantID, fileID uuid.UUID) string {
	return "private/" + tenantID.String() + "/mail-files/" + fileID.String()
}

// UploadOptions son las elecciones del remitente; 0 toma el valor por defecto de la politica.
type UploadOptions struct {
	ExpiresInDays int
	MaxDownloads  int
}

// Policy son los topes que aplica el servicio. Llegan de la configuracion y se sirven a la interfaz.
type Policy struct {
	MaxFileBytes        int64
	DefaultExpiryDays   int
	MaxExpiryDays       int
	DefaultMaxDownloads int
	MaxDownloads        int
	MailboxQuotaBytes   int64
	TenantQuotaBytes    int64
	MaxActivePerMailbox int
}

// Validate rechaza una politica incoherente: un valor por defecto fuera de su tope o un fichero que
// no cabria en ninguna cuota.
func (p Policy) Validate() error {
	switch {
	case p.MaxFileBytes <= 0:
		return errors.New("el tamano maximo de fichero debe ser positivo")
	case p.MaxExpiryDays < 1 || p.DefaultExpiryDays < 1 || p.DefaultExpiryDays > p.MaxExpiryDays:
		return fmt.Errorf("caducidad por defecto (%d dias) fuera de 1..%d", p.DefaultExpiryDays, p.MaxExpiryDays)
	case p.MaxDownloads < 1 || p.DefaultMaxDownloads < 1 || p.DefaultMaxDownloads > p.MaxDownloads:
		return fmt.Errorf("descargas por defecto (%d) fuera de 1..%d", p.DefaultMaxDownloads, p.MaxDownloads)
	case p.MailboxQuotaBytes < p.MaxFileBytes || p.TenantQuotaBytes < p.MailboxQuotaBytes:
		return errors.New("las cuotas deben cumplir fichero maximo <= cuota del buzon <= cuota de la empresa")
	case p.MaxActivePerMailbox < 1:
		return errors.New("el maximo de enlaces vigentes por buzon debe ser positivo")
	}
	return nil
}

// Resolve aplica los valores por defecto y los topes a las elecciones del remitente.
func (p Policy) Resolve(o UploadOptions) (expiresIn time.Duration, maxDownloads int, err error) {
	days := o.ExpiresInDays
	if days == 0 {
		days = p.DefaultExpiryDays
	}
	if days < 1 || days > p.MaxExpiryDays {
		return 0, 0, NewValidationError("expires_in_days", fmt.Sprintf("la caducidad debe estar entre 1 y %d dias", p.MaxExpiryDays))
	}
	maxDownloads = o.MaxDownloads
	if maxDownloads == 0 {
		maxDownloads = p.DefaultMaxDownloads
	}
	if maxDownloads < 1 || maxDownloads > p.MaxDownloads {
		return 0, 0, NewValidationError("max_downloads", fmt.Sprintf("las descargas deben estar entre 1 y %d", p.MaxDownloads))
	}
	return time.Duration(days) * 24 * time.Hour, maxDownloads, nil
}

// Usage es lo que ocupan los enlaces vigentes (subiendo o descargables) del buzon y de la empresa.
type Usage struct {
	MailboxBytes  int64
	MailboxActive int
	TenantBytes   int64
}

// Admits dice si un fichero de size bytes cabe con el uso actual; el error nombra la cuota que se
// supera.
func (p Policy) Admits(u Usage, size int64) error {
	switch {
	case u.MailboxActive >= p.MaxActivePerMailbox:
		return ErrTooManyFiles
	case u.MailboxBytes+size > p.MailboxQuotaBytes:
		return ErrMailboxQuota
	case u.TenantBytes+size > p.TenantQuotaBytes:
		return ErrTenantQuota
	}
	return nil
}

// SharedFile es un fichero con su enlace publico, lo que ve el remitente.
type SharedFile struct {
	File
	URL string
}

// Listing es la vista del remitente: sus enlaces, su uso y la politica que se le aplica.
type Listing struct {
	Items  []SharedFile
	Usage  Usage
	Policy Policy
}
