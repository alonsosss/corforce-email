package domain

import (
	"errors"
	"time"
)

// Ficheros grandes por enlace (docs/adr/0014). Los guarda mail-files en la base de la empresa y en el
// almacen de objetos; el webmail solo los lleva con la empresa y el buzon de la sesion. Topes, cuotas,
// caducidad y descargas son de mail-files y llegan con cada listado.

var (
	// ErrLargeFilesDisabled: este webmail no tiene mail-files configurado.
	ErrLargeFilesDisabled = errors.New("el envio de ficheros grandes no esta disponible")
	// ErrLargeFileTooLarge: la subida supera lo que el webmail deja pasar hacia mail-files.
	ErrLargeFileTooLarge = errors.New("el fichero supera el tamano maximo")
)

// LargeFileLimits son los topes que aplica mail-files.
type LargeFileLimits struct {
	MaxFileBytes        int64
	DefaultExpiryDays   int
	MaxExpiryDays       int
	DefaultMaxDownloads int
	MaxDownloads        int
	MailboxQuotaBytes   int64
	TenantQuotaBytes    int64
	MaxActivePerMailbox int
}

// LargeFileUsage es lo que ocupan los enlaces vigentes del buzon y de su empresa.
type LargeFileUsage struct {
	MailboxBytes  int64
	MailboxActive int
	TenantBytes   int64
}

// LargeFile es un fichero compartido por el buzon. URL solo viene mientras el enlace sirve.
type LargeFile struct {
	ID                 string
	Name               string
	SizeBytes          int64
	SHA256             string
	URL                string
	State              string
	ExpiresAt          time.Time
	MaxDownloads       int
	Downloads          int
	RemainingDownloads int
	CreatedAt          time.Time
	LastDownloadAt     *time.Time
	RevokedAt          *time.Time
}

// LargeFileListing es lo que ve el remitente: si la funcion esta disponible, sus enlaces, su uso y los
// topes.
type LargeFileListing struct {
	Enabled bool
	Items   []LargeFile
	Usage   LargeFileUsage
	Limits  LargeFileLimits
}

// LargeFileOptions son las elecciones del remitente; 0 deja el valor por defecto de mail-files.
type LargeFileOptions struct {
	ExpiresInDays int
	MaxDownloads  int
}
