package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	// ErrMFARequired es una accion que exige el codigo de verificacion y llego sin el.
	ErrMFARequired = errors.New("se requiere el código de verificación")
	// ErrInvalidMFACode es un codigo (TOTP o de recuperacion) que no vale.
	ErrInvalidMFACode = errors.New("el código de verificación no es válido")
	// ErrMFAChallengeExpired es un segundo paso del inicio de sesion sin desafio vivo: caducado,
	// agotado o inexistente. Hay que volver a escribir la contrasena.
	ErrMFAChallengeExpired = errors.New("la verificación caducó; vuelve a iniciar sesión")
	// ErrMFAAlreadyEnabled y ErrMFANotEnabled son un estado de la verificacion en dos pasos que no
	// admite lo pedido.
	ErrMFAAlreadyEnabled = errors.New("la verificación en dos pasos ya está activa")
	ErrMFANotEnabled     = errors.New("la verificación en dos pasos no está activa")
	// ErrMFASetupExpired es una activacion con un secreto que no salio de una preparacion reciente
	// del mismo buzon, la unica que comprueba la contrasena.
	ErrMFASetupExpired = errors.New("la preparación de la verificación caducó; vuelve a empezar")

	ErrAppPasswordNotFound = errors.New("contraseña de aplicación no encontrada")
	ErrAppPasswordLimit    = errors.New("el buzón alcanzó su máximo de contraseñas de aplicación")
)

// MaxMFACodeLen acota lo que se acepta como codigo: un TOTP son 6 digitos y uno de recuperacion
// 10 caracteres en dos grupos. Lo que pase de aqui no llega al directorio.
const MaxMFACodeLen = 32

// NormalizeMFACode quita los espacios de alrededor; ok es false si no puede ser un codigo.
func NormalizeMFACode(raw string) (string, bool) {
	code := strings.TrimSpace(raw)
	return code, code != "" && len(code) <= MaxMFACodeLen
}

// ReauthRequiredError es un cambio que mail-directory solo acepta tras volver a confirmar la
// contrasena (y el codigo con verificacion en dos pasos): reenviar a direcciones de fuera de la
// empresa. Addresses son los destinos nuevos que lo exigen.
type ReauthRequiredError struct {
	Addresses []string
}

func (e *ReauthRequiredError) Error() string {
	return "confirma tu contraseña para reenviar a direcciones externas"
}

// ExternalForwardingDisabledError es un reenvio a direcciones externas que la empresa prohibe.
type ExternalForwardingDisabledError struct {
	Addresses []string
}

func (e *ExternalForwardingDisabledError) Error() string {
	return "tu empresa no permite reenviar el correo a direcciones externas"
}

// MFAChallenge es el primer paso de un inicio de sesion con verificacion en dos pasos: la identidad
// que mail-auth confirmo con la contrasena, a la espera del codigo. StartedAt es cuando empezo esa
// verificacion y la sesion que se abra lo hereda como CreatedAt: una revocacion del buzon posterior
// tambien la alcanza.
type MFAChallenge struct {
	Identity  Identity
	StartedAt time.Time
}

// Reauthentication es la confirmacion de identidad que acompana a una accion sensible.
type Reauthentication struct {
	CurrentPassword string
	Code            string
}

// MFAStatus es el estado de la verificacion en dos pasos del buzon.
type MFAStatus struct {
	Enabled           bool
	EnabledAt         *time.Time
	RecoveryRemaining int
}

// MFASetup es un secreto nuevo, aun sin guardar, y su URI otpauth:// para el codigo QR.
type MFASetup struct {
	Secret          string
	ProvisioningURI string
}

// MFAVerification es un codigo aceptado: Method es "totp" o "recovery".
type MFAVerification struct {
	Method            string
	RecoveryRemaining int
}

// AppPasswordAccess son los protocolos que abre una contrasena de aplicacion.
type AppPasswordAccess struct {
	IMAP  bool
	POP3  bool
	SMTP  bool
	Sieve bool
	DAV   bool
}

// Any dice si abre al menos un protocolo.
func (a AppPasswordAccess) Any() bool { return a.IMAP || a.POP3 || a.SMTP || a.Sieve || a.DAV }

// AppPassword es una contrasena de aplicacion del buzon, sin su hash.
type AppPassword struct {
	ID         string
	Name       string
	Access     AppPasswordAccess
	Active     bool
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// AppPasswordInput es una contrasena de aplicacion nueva.
type AppPasswordInput struct {
	Name   string
	Access AppPasswordAccess
}

// CreatedAppPassword es la contrasena recien creada con su valor en claro, que se muestra una vez.
type CreatedAppPassword struct {
	AppPassword
	Password string
}

// AppPasswordList son las contrasenas de aplicacion del buzon. Max es el tope que informa
// mail-directory; 0 si no lo informa.
type AppPasswordList struct {
	Items []AppPassword
	Max   int
}

// SecurityOverview es lo que muestra la pestana de seguridad del webmail.
type SecurityOverview struct {
	MFA          MFAStatus
	AppPasswords AppPasswordList
}
