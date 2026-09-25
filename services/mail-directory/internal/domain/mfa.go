package domain

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Verificacion en dos pasos del buzon (docs/Plan_Webmail_Seguridad.md, decisiones 1, 5 y 6).
const (
	// RecoveryCodeCount es cuantos codigos de recuperacion se entregan al activar o regenerar.
	RecoveryCodeCount = 10
	// RecoveryCodeLength son los caracteres de un codigo, sin el guion que lo parte en dos grupos.
	RecoveryCodeLength = 10
	// RecoveryCodeAlphabet es base32 sin los caracteres que se confunden al teclearlos (I, O, 0, 1):
	// 32 simbolos, 5 bits cada uno, 50 bits por codigo. Al ser aleatorios de 50 bits basta un SHA-256
	// para guardarlos; un hash lento no anade nada frente a un espacio asi.
	RecoveryCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	recoveryGroup        = RecoveryCodeLength / 2

	// totpCodeLength son las cifras de un codigo TOTP (pkg/totp).
	totpCodeLength = 6
	// Un secreto TOTP en base32 de 16 a 64 bytes: identity y las aplicaciones usan 20 (160 bits).
	minTOTPSecretBytes = 16
	maxTOTPSecretBytes = 64
)

// MFASecretAAD son los datos autenticados con que se cifra el secreto TOTP del buzon: su id. El
// cifrado de un buzon copiado a otro no se abre.
func MFASecretAAD(mailboxID uuid.UUID) []byte { return mailboxID[:] }

// Con que se supero el segundo paso.
const (
	MFAMethodTOTP     = "totp"
	MFAMethodRecovery = "recovery"
)

// Quien apaga la verificacion: el propio buzon desde el webmail o el administrador que la restablece.
const (
	MFADisabledByUser  = "user"
	MFADisabledByAdmin = "admin"
)

// MailboxMFA es la verificacion en dos pasos activa de un buzon (mail.mailbox_mfa). Solo existe
// mientras esta activa; SecretEnc es el secreto TOTP cifrado y nunca sale del servicio.
type MailboxMFA struct {
	MailboxID      uuid.UUID
	TenantID       uuid.UUID
	SecretEnc      []byte
	RecoveryHashes []string
	LastStep       int64
	EnabledAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// MFAStatus es lo que el webmail muestra de la verificacion del buzon.
type MFAStatus struct {
	Enabled           bool
	EnabledAt         *time.Time
	RecoveryRemaining int
}

// MFAVerification es el desenlace de un codigo aceptado.
type MFAVerification struct {
	Method            string
	RecoveryRemaining int
}

// Status resume la verificacion; nil es un buzon sin ella.
func (m *MailboxMFA) Status() MFAStatus {
	if m == nil {
		return MFAStatus{}
	}
	at := m.EnabledAt.UTC()
	return MFAStatus{Enabled: true, EnabledAt: &at, RecoveryRemaining: len(m.RecoveryHashes)}
}

// NormalizeMFACode limpia lo que teclea una persona: sin espacios alrededor ni dentro, sin guiones y
// en mayusculas. Vale para un codigo TOTP y para uno de recuperacion.
func NormalizeMFACode(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch r {
		case ' ', '-', '\t':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// IsTOTPCode dice si un codigo ya normalizado tiene la forma de uno TOTP: seis cifras.
func IsTOTPCode(code string) bool {
	if len(code) != totpCodeLength {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// IsRecoveryCode dice si un codigo ya normalizado tiene la forma de uno de recuperacion.
func IsRecoveryCode(code string) bool {
	if len(code) != RecoveryCodeLength {
		return false
	}
	for _, r := range code {
		if !strings.ContainsRune(RecoveryCodeAlphabet, r) {
			return false
		}
	}
	return true
}

// FormatRecoveryCode presenta un codigo de recuperacion en dos grupos, XXXXX-XXXXX.
func FormatRecoveryCode(code string) string {
	if len(code) != RecoveryCodeLength {
		return code
	}
	return code[:recoveryGroup] + "-" + code[recoveryGroup:]
}

// HashRecoveryCode es el SHA-256 en hexadecimal de un codigo ya normalizado: lo que se guarda.
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// NormalizeTOTPSecret valida el secreto que el webmail genero en setup y devuelve su forma canonica:
// base32 sin relleno, en mayusculas y sin espacios.
func NormalizeTOTPSecret(raw string) (string, error) {
	secret := strings.TrimRight(NormalizeMFACode(raw), "=")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(key) < minTOTPSecretBytes || len(key) > maxTOTPSecretBytes {
		return "", fieldErr("secret", "no es un secreto TOTP válido")
	}
	return secret, nil
}

var (
	// ErrInvalidMFACode: el codigo no coincide, ya se uso o no tiene la forma de ninguno.
	ErrInvalidMFACode = errors.New("el código de verificación no es válido")
	// ErrMFAAlreadyEnabled: se intenta activar la verificacion en un buzon que ya la tiene.
	ErrMFAAlreadyEnabled = errors.New("la verificación en dos pasos ya está activada")
	// ErrMFANotEnabled: se opera sobre la verificacion de un buzon que no la tiene.
	ErrMFANotEnabled = errors.New("la verificación en dos pasos no está activada")
)
