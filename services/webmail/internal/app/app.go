// Package app orquesta el webmail: sesiones sobre mail-auth y Redis, lectura y
// organizacion del buzon por IMAP y envio por el submission de la celda.
package app

import (
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// Config son la celda, la politica de sesion y los topes del webmail.
type Config struct {
	// CellCode es la celda de esta instancia: va en cada token de sesion que abre, y solo los
	// tokens de esta celda se buscan en el almacen.
	CellCode           string
	Sessions           domain.SessionPolicy
	Limits             domain.Limits
	MaxBodyPartBytes   int64
	MaxAttachmentBytes int64
	// SendTimeout acota un envio de principio a fin (adjuntos del buzon, ClamAV y SMTP); de
	// el sale cuanto vive la reserva de su clave de idempotencia.
	SendTimeout time.Duration
}

type Deps struct {
	Auth      ports.Authenticator
	Sessions  ports.SessionStore
	Mail      ports.MailStore
	Sender    ports.Sender
	Directory ports.SenderDirectory
	Vacations ports.VacationDirectory
	Ledger    ports.SendLedger
	Composer  ports.Composer
	Sanitizer ports.HTMLSanitizer
	// Scanner solo puede faltar si main lo decidio de forma explicita (desarrollo sin
	// ClamAV); en ese caso los adjuntos se aceptan sin analizar.
	Scanner ports.VirusScanner
	PartURL ports.PartURL
	Clock   func() time.Time
	Logger  *zap.Logger
	Config  Config
}

// Service es el caso de uso del webmail.
type Service struct {
	auth      ports.Authenticator
	sessions  ports.SessionStore
	mail      ports.MailStore
	sender    ports.Sender
	directory ports.SenderDirectory
	vacations ports.VacationDirectory
	ledger    ports.SendLedger
	composer  ports.Composer
	sanitizer ports.HTMLSanitizer
	scanner   ports.VirusScanner
	partURL   ports.PartURL
	clock     func() time.Time
	logger    *zap.Logger
	cfg       Config
}

// New valida la configuracion y las dependencias: un webmail a medio cablear no arranca.
func New(d Deps) (*Service, error) {
	if d.Auth == nil || d.Sessions == nil || d.Mail == nil || d.Sender == nil || d.Directory == nil || d.Vacations == nil ||
		d.Ledger == nil || d.Composer == nil || d.Sanitizer == nil || d.PartURL == nil || d.Logger == nil {
		return nil, errors.New("webmail: faltan dependencias del caso de uso")
	}
	if !domain.ValidCellCode(d.Config.CellCode) {
		return nil, fmt.Errorf("webmail: %q no es un codigo de celda", d.Config.CellCode)
	}
	if d.Config.SendTimeout <= 0 {
		return nil, errors.New("webmail: el plazo de envio debe ser positivo")
	}
	if err := d.Config.Sessions.Validate(); err != nil {
		return nil, err
	}
	if err := d.Config.Limits.Validate(); err != nil {
		return nil, err
	}
	if d.Config.MaxBodyPartBytes < 1 || d.Config.MaxAttachmentBytes < 1 {
		return nil, fmt.Errorf("webmail: los topes de lectura deben ser positivos")
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		auth: d.Auth, sessions: d.Sessions, mail: d.Mail, sender: d.Sender, directory: d.Directory, vacations: d.Vacations,
		ledger: d.Ledger, composer: d.Composer, sanitizer: d.Sanitizer, scanner: d.Scanner,
		partURL: d.PartURL, clock: clock, logger: d.Logger, cfg: d.Config,
	}, nil
}

// unavailable envuelve un fallo de infraestructura para que el adaptador HTTP lo
// traduzca a 503 sin perder el motivo en el log.
func unavailable(err error) error {
	if err == nil || errors.Is(err, domain.ErrUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
}
