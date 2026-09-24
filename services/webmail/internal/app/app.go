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
	// MaxScheduledDays es lo mas lejos que se puede programar un envio.
	MaxScheduledDays int
	// ScheduledPollInterval y ScheduledBatch rigen el trabajador de envios programados: cada
	// cuanto reclama filas vencidas y cuantas a la vez.
	ScheduledPollInterval time.Duration
	ScheduledBatch        int
	// MaxImportBytes acota el fichero vCard que se importa a la libreta personal.
	MaxImportBytes int64
	// MaxReminderDays es lo mas lejos que se puede posponer un mensaje o fijar un seguimiento;
	// ReminderPollInterval y ReminderBatch rigen el trabajador de recordatorios como los de programados.
	MaxReminderDays      int
	ReminderPollInterval time.Duration
	ReminderBatch        int
}

type Deps struct {
	Auth        ports.Authenticator
	Sessions    ports.SessionStore
	Mail        ports.MailStore
	Sender      ports.Sender
	Directory   ports.SenderDirectory
	Vacations   ports.VacationDirectory
	AddressBook ports.AddressBook
	Signatures  ports.SignatureDirectory
	Filters     ports.FilterDirectory
	Passwords   ports.PasswordDirectory
	Scheduled   ports.ScheduledDirectory
	Contacts    ports.ContactBook
	Calendar    ports.Calendar
	// Watcher es opcional: sin el, GET /events responde que los avisos estan desactivados y la interfaz
	// refresca por sondeo.
	Watcher   ports.MailboxWatcher
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

	// Unsubscriber hace la baja en un clic (RFC 8058) de los boletines.
	Unsubscriber ports.Unsubscriber
	// Recordatorios (posponer y seguimiento) y respuestas rapidas del buzon, en mail-directory.
	Reminders    ports.ReminderDirectory
	QuickReplies ports.QuickReplyDirectory
}

// Service es el caso de uso del webmail.
type Service struct {
	auth        ports.Authenticator
	sessions    ports.SessionStore
	mail        ports.MailStore
	sender      ports.Sender
	directory   ports.SenderDirectory
	vacations   ports.VacationDirectory
	addressBook ports.AddressBook
	signatures  ports.SignatureDirectory
	filters     ports.FilterDirectory
	passwords   ports.PasswordDirectory
	scheduled   ports.ScheduledDirectory
	contacts    ports.ContactBook
	calendar    ports.Calendar
	watcher     ports.MailboxWatcher
	ledger      ports.SendLedger
	composer    ports.Composer
	sanitizer   ports.HTMLSanitizer
	scanner     ports.VirusScanner
	partURL     ports.PartURL
	clock       func() time.Time
	logger      *zap.Logger
	cfg         Config

	unsubscriber ports.Unsubscriber
	// Recordatorios y respuestas rapidas (Deps.Reminders, Deps.QuickReplies).
	reminders    ports.ReminderDirectory
	quickReplies ports.QuickReplyDirectory
}

// New valida la configuracion y las dependencias: un webmail a medio cablear no arranca.
func New(d Deps) (*Service, error) {
	if d.Auth == nil || d.Sessions == nil || d.Mail == nil || d.Sender == nil || d.Directory == nil || d.Vacations == nil || d.AddressBook == nil ||
		d.Signatures == nil || d.Filters == nil || d.Passwords == nil || d.Scheduled == nil || d.Contacts == nil || d.Calendar == nil ||
		d.Unsubscriber == nil || d.Ledger == nil || d.Composer == nil || d.Sanitizer == nil || d.PartURL == nil || d.Logger == nil {
		return nil, errors.New("webmail: faltan dependencias del caso de uso")
	}
	if d.Reminders == nil || d.QuickReplies == nil {
		return nil, errors.New("webmail: faltan los recordatorios o las respuestas rapidas del directorio")
	}
	if d.Config.MaxReminderDays < 1 || d.Config.ReminderPollInterval <= 0 || d.Config.ReminderBatch < 1 {
		return nil, errors.New("webmail: el plazo, el intervalo y el lote de los recordatorios deben ser positivos")
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
	if d.Config.MaxScheduledDays < 1 || d.Config.ScheduledPollInterval <= 0 || d.Config.ScheduledBatch < 1 || d.Config.MaxImportBytes < 1 {
		return nil, errors.New("webmail: el plazo de programacion, el intervalo y el lote del trabajador y el tope de importacion deben ser positivos")
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		auth: d.Auth, sessions: d.Sessions, mail: d.Mail, sender: d.Sender, directory: d.Directory, vacations: d.Vacations, addressBook: d.AddressBook, watcher: d.Watcher,
		signatures: d.Signatures, filters: d.Filters, passwords: d.Passwords, scheduled: d.Scheduled, contacts: d.Contacts, calendar: d.Calendar,
		ledger: d.Ledger, composer: d.Composer, sanitizer: d.Sanitizer, scanner: d.Scanner,
		partURL: d.PartURL, clock: clock, logger: d.Logger, cfg: d.Config, unsubscriber: d.Unsubscriber,
		reminders: d.Reminders, quickReplies: d.QuickReplies,
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
