package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Statuses lista los estados validos, para filtrar el listado.
func Statuses() []Status {
	return []Status{StatusPending, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled}
}

func ParseStatus(raw string) (Status, bool) {
	for _, s := range Statuses() {
		if string(s) == raw {
			return s, true
		}
	}
	return "", false
}

// Active es un trabajo que aun ocupa el buzon y cuenta para el limite de la empresa.
func (s Status) Active() bool { return s == StatusPending || s == StatusRunning }

// Phase es la pasada de imapsync en curso: la inicial copia todo y la de repaso recoge lo que
// llego mientras tanto.
type Phase string

const (
	PhaseNone    Phase = ""
	PhaseInitial Phase = "initial"
	PhaseCatchup Phase = "catchup"
)

func Phases() []Phase { return []Phase{PhaseInitial, PhaseCatchup} }

func ParsePhase(raw string) (Phase, bool) {
	for _, p := range Phases() {
		if string(p) == raw {
			return p, true
		}
	}
	return PhaseNone, false
}

// ErrorCode clasifica por que termino mal un trabajo. Es el vocabulario comun del ejecutor, la API y
// la interfaz; el mensaje que lo acompana es solo apoyo.
type ErrorCode string

const (
	CodeSourceAuthFailed     ErrorCode = "source_auth_failed"
	CodeSourceUnreachable    ErrorCode = "source_unreachable"
	CodeSourceBlockedAddress ErrorCode = "source_blocked_address"
	CodeSourceTLSFailed      ErrorCode = "source_tls_failed"
	CodeDestinationFailed    ErrorCode = "destination_failed"
	CodeQuotaExceeded        ErrorCode = "quota_exceeded"
	CodeTimeout              ErrorCode = "timeout"
	CodeVirusFound           ErrorCode = "virus_found"
	CodeImapsyncFailed       ErrorCode = "imapsync_failed"
	CodeRunnerLost           ErrorCode = "runner_lost"
	CodeCredentialUnreadable ErrorCode = "credential_unreadable"
	CodeCancelled            ErrorCode = "cancelled"
	// CodeMailboxDeleted cierra un trabajo cuyo buzon destino se borro en mail-directory. Solo lo
	// anuncia el servicio a la auditoria: la fila se borra en la misma transaccion.
	CodeMailboxDeleted ErrorCode = "mailbox_deleted"
)

var errorCodes = []ErrorCode{
	CodeSourceAuthFailed, CodeSourceUnreachable, CodeSourceBlockedAddress, CodeSourceTLSFailed,
	CodeDestinationFailed, CodeQuotaExceeded, CodeTimeout, CodeVirusFound, CodeImapsyncFailed,
	CodeRunnerLost, CodeCredentialUnreadable, CodeCancelled, CodeMailboxDeleted,
}

func (c ErrorCode) Valid() bool {
	for _, known := range errorCodes {
		if c == known {
			return true
		}
	}
	return false
}

const (
	maxErrorMessageRunes = 300
	// MaxFolders y maxFolderNameRunes acotan lo que el ejecutor puede hacer crecer en la fila.
	MaxFolders            = 500
	maxFolderNameRunes    = 200
	runnerLostMessage     = "el ejecutor dejo de responder y se agotaron los reintentos"
	credentialLostMessage = "la credencial de origen no se pudo descifrar con las claves configuradas"
)

// JobError es el ultimo motivo de fallo, saneado: sin contenido de correo ni credenciales.
type JobError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// NewJobError sanea el mensaje que informa el ejecutor: quita los caracteres de control, retira
// cada secreto que aparezca (la contrasena de origen, por si un servidor la repite en su error) y
// recorta a un tamano fijo. Un codigo desconocido se reduce a imapsync_failed.
func NewJobError(code ErrorCode, message string, secrets ...string) *JobError {
	if !code.Valid() {
		code = CodeImapsyncFailed
	}
	return &JobError{Code: code, Message: sanitizeMessage(message, secrets)}
}

func RunnerLostError() *JobError { return &JobError{Code: CodeRunnerLost, Message: runnerLostMessage} }

func CredentialUnreadableError() *JobError {
	return &JobError{Code: CodeCredentialUnreadable, Message: credentialLostMessage}
}

func sanitizeMessage(message string, secrets []string) string {
	for _, s := range secrets {
		if s != "" {
			message = strings.ReplaceAll(message, s, "***")
		}
	}
	var b strings.Builder
	for _, r := range message {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > maxErrorMessageRunes {
		out = string([]rune(out)[:maxErrorMessageRunes])
	}
	return out
}

// FolderProgress son los contadores de una carpeta; el nombre es el del origen, nunca contenido.
type FolderProgress struct {
	Name            string `json:"name"`
	MessagesCopied  int64  `json:"messages_copied"`
	MessagesSkipped int64  `json:"messages_skipped"`
	MessagesFailed  int64  `json:"messages_failed"`
}

// Progress es lo que el ejecutor informa en cada latido: totales y detalle por carpeta.
type Progress struct {
	FoldersTotal    int64            `json:"folders_total"`
	FoldersDone     int64            `json:"folders_done"`
	MessagesTotal   int64            `json:"messages_total"`
	MessagesCopied  int64            `json:"messages_copied"`
	MessagesSkipped int64            `json:"messages_skipped"`
	MessagesFailed  int64            `json:"messages_failed"`
	BytesCopied     int64            `json:"bytes_copied"`
	Folders         []FolderProgress `json:"folders"`
}

// Normalize rechaza contadores negativos y acota el detalle por carpeta. Deja siempre una lista no
// nula, para que serialice como [].
func (p *Progress) Normalize() error {
	for _, v := range []int64{p.FoldersTotal, p.FoldersDone, p.MessagesTotal, p.MessagesCopied,
		p.MessagesSkipped, p.MessagesFailed, p.BytesCopied} {
		if v < 0 {
			return ErrInvalidProgress
		}
	}
	if len(p.Folders) > MaxFolders {
		p.Folders = p.Folders[:MaxFolders]
	}
	folders := make([]FolderProgress, 0, len(p.Folders))
	for _, f := range p.Folders {
		if f.MessagesCopied < 0 || f.MessagesSkipped < 0 || f.MessagesFailed < 0 {
			return ErrInvalidProgress
		}
		f.Name = sanitizeMessage(f.Name, nil)
		if utf8.RuneCountInString(f.Name) > maxFolderNameRunes {
			f.Name = string([]rune(f.Name)[:maxFolderNameRunes])
		}
		folders = append(folders, f)
	}
	p.Folders = folders
	return nil
}

// Job es una migracion de un buzon. SourcePasswordEnc solo existe mientras el trabajo esta activo y
// nunca se serializa.
type Job struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	MailboxID         uuid.UUID
	MailboxUsername   string
	SourceHost        string
	SourcePort        int
	SourceTLS         TLSMode
	SourceUsername    string
	SourcePasswordEnc []byte
	Status            Status
	Phase             Phase
	Progress          Progress
	Attempt           int
	LastError         *JobError
	RequestedBy       uuid.UUID
	RunnerID          string
	LeaseID           *uuid.UUID
	LeaseExpiresAt    *time.Time
	HeartbeatAt       *time.Time
	CancelRequestedAt *time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// CancelForDeletedMailbox deja el trabajo como lo anuncia la auditoria cuando su buzon destino se
// borra: cancelado por el servicio, sin credencial ni lease.
func (j *Job) CancelForDeletedMailbox(at time.Time) {
	j.Status = StatusCancelled
	j.LastError = &JobError{Code: CodeMailboxDeleted}
	j.SourcePasswordEnc = nil
	j.LeaseID, j.LeaseExpiresAt = nil, nil
	j.FinishedAt = &at
}

// Outcome es como cierra el ejecutor un trabajo.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

func ParseOutcome(raw string) (Outcome, error) {
	switch Outcome(raw) {
	case OutcomeSucceeded, OutcomeFailed, OutcomeCancelled:
		return Outcome(raw), nil
	}
	return "", ErrInvalidOutcome
}

// Status es el estado final que corresponde al resultado.
func (o Outcome) Status() Status {
	switch o {
	case OutcomeSucceeded:
		return StatusSucceeded
	case OutcomeCancelled:
		return StatusCancelled
	default:
		return StatusFailed
	}
}
