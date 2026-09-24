package app

import (
	"context"
	"errors"
	"io"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// La libreta personal y el calendario viven en mail-dav, en la base de la empresa, con los mismos
// casos de uso que CardDAV y CalDAV. El webmail los pide con la empresa y el buzon de la sesion,
// nunca con datos de la peticion.

// mailboxOf exige la empresa y el buzon de la sesion; sin ellos la sesion es anterior a que
// mail-auth los devolviera y el usuario debe volver a entrar.
func mailboxOf(sess domain.Session) (domain.MailboxRef, error) {
	mb, ok := sess.Mailbox()
	if !ok {
		return domain.MailboxRef{}, domain.ErrSessionInvalid
	}
	return mb, nil
}

// davError deja pasar tal cual lo que rechaza mail-dav (su codigo y sus detalles llegan al usuario)
// y convierte un fallo de la llamada en indisponibilidad.
func (s *Service) davError(msg string, sess domain.Session, err error) error {
	var rejection *domain.ServiceRejection
	if errors.As(err, &rejection) {
		if rejection.Kind == domain.RejectUnavailable {
			s.logFailure(msg, sess, err)
		}
		return err
	}
	s.logFailure(msg, sess, err)
	return unavailable(err)
}

// DAVLimits son los topes de la libreta y del calendario (mail-dav), para que la interfaz no los
// copie.
func (s *Service) DAVLimits(ctx context.Context, sess domain.Session) (map[string]int64, error) {
	if _, err := mailboxOf(sess); err != nil {
		return nil, err
	}
	limits, err := s.contacts.Limits(ctx)
	if err != nil {
		return nil, s.davError("no se pudieron leer los topes de mail-dav", sess, err)
	}
	return limits, nil
}

func (s *Service) ListContacts(ctx context.Context, sess domain.Session, q domain.ContactQuery) (domain.ContactPage, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.ContactPage{}, err
	}
	page, err := s.contacts.ListContacts(ctx, mb, q)
	if err != nil {
		return domain.ContactPage{}, s.davError("no se pudo leer la libreta personal", sess, err)
	}
	return page, nil
}

func (s *Service) Contact(ctx context.Context, sess domain.Session, id string) (domain.Contact, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.Contact{}, err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return domain.Contact{}, err
	}
	c, err := s.contacts.Contact(ctx, mb, id)
	if err != nil {
		return domain.Contact{}, s.davError("no se pudo leer el contacto", sess, err)
	}
	return c, nil
}

func (s *Service) CreateContact(ctx context.Context, sess domain.Session, in domain.ContactInput) (domain.Contact, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.Contact{}, err
	}
	c, err := s.contacts.CreateContact(ctx, mb, in)
	if err != nil {
		return domain.Contact{}, s.davError("no se pudo crear el contacto", sess, err)
	}
	return c, nil
}

func (s *Service) UpdateContact(ctx context.Context, sess domain.Session, id string, in domain.ContactInput, ifMatch string) (domain.Contact, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.Contact{}, err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return domain.Contact{}, err
	}
	if err := domain.ValidateIfMatch(ifMatch); err != nil {
		return domain.Contact{}, err
	}
	c, err := s.contacts.UpdateContact(ctx, mb, id, in, ifMatch)
	if err != nil {
		return domain.Contact{}, s.davError("no se pudo guardar el contacto", sess, err)
	}
	return c, nil
}

func (s *Service) DeleteContact(ctx context.Context, sess domain.Session, id string) error {
	mb, err := mailboxOf(sess)
	if err != nil {
		return err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return err
	}
	if err := s.contacts.DeleteContact(ctx, mb, id); err != nil {
		return s.davError("no se pudo borrar el contacto", sess, err)
	}
	return nil
}

// ExportContacts entrega todas las tarjetas a emit, que escribe la respuesta.
func (s *Service) ExportContacts(ctx context.Context, sess domain.Session, emit func(io.Reader) error) error {
	mb, err := mailboxOf(sess)
	if err != nil {
		return err
	}
	body, err := s.contacts.ExportContacts(ctx, mb)
	if err != nil {
		return s.davError("no se pudo exportar la libreta personal", sess, err)
	}
	defer body.Close()
	return emit(body)
}

// ImportContacts importa un fichero vCard con varias tarjetas. El tope lo aplica el webmail antes
// de reenviarlo; las tarjetas las valida mail-dav.
func (s *Service) ImportContacts(ctx context.Context, sess domain.Session, filename string, data []byte) (domain.ImportResult, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.ImportResult{}, err
	}
	if len(data) == 0 {
		return domain.ImportResult{}, domain.NewValidationError("file", "el fichero esta vacio")
	}
	if int64(len(data)) > s.cfg.MaxImportBytes {
		return domain.ImportResult{}, domain.ErrImportTooLarge
	}
	res, err := s.contacts.ImportContacts(ctx, mb, domain.SanitizeFilename(filename), data)
	if err != nil {
		return domain.ImportResult{}, s.davError("no se pudo importar la libreta", sess, err)
	}
	return res, nil
}

func (s *Service) Occurrences(ctx context.Context, sess domain.Session, w domain.EventWindow) ([]domain.Occurrence, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return nil, err
	}
	out, err := s.calendar.Occurrences(ctx, mb, w)
	if err != nil {
		return nil, s.davError("no se pudo leer el calendario", sess, err)
	}
	return out, nil
}

func (s *Service) Event(ctx context.Context, sess domain.Session, id string) (domain.Event, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.Event{}, err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return domain.Event{}, err
	}
	e, err := s.calendar.Event(ctx, mb, id)
	if err != nil {
		return domain.Event{}, s.davError("no se pudo leer el evento", sess, err)
	}
	return e, nil
}

// CreateEvent crea el evento; con invitados, el organizador es el buzon de la sesion y, si notify, la invitacion
// (REQUEST) sale desde el buzon por el mismo camino que un correo.
func (s *Service) CreateEvent(ctx context.Context, sess domain.Session, in domain.EventInput, notify bool) (domain.SavedEvent, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.SavedEvent{}, err
	}
	s.asOrganizer(sess, &in)
	e, err := s.calendar.CreateEvent(ctx, mb, in)
	if err != nil {
		return domain.SavedEvent{}, s.davError("no se pudo crear el evento", sess, err)
	}
	return s.notifyChange(ctx, sess, mb, e, notify), nil
}

// UpdateEvent guarda el evento; con invitados y notify, envia la invitacion actualizada (REQUEST).
func (s *Service) UpdateEvent(ctx context.Context, sess domain.Session, id string, in domain.EventInput, ifMatch string, notify bool) (domain.SavedEvent, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.SavedEvent{}, err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return domain.SavedEvent{}, err
	}
	if err := domain.ValidateIfMatch(ifMatch); err != nil {
		return domain.SavedEvent{}, err
	}
	s.asOrganizer(sess, &in)
	e, err := s.calendar.UpdateEvent(ctx, mb, id, in, ifMatch)
	if err != nil {
		return domain.SavedEvent{}, s.davError("no se pudo guardar el evento", sess, err)
	}
	return s.notifyChange(ctx, sess, mb, e, notify), nil
}

// DeleteEvent borra el evento entero; si el buzon lo organizaba con invitados y notify, les envia la cancelacion
// (CANCEL), que se escribe antes de borrar.
func (s *Service) DeleteEvent(ctx context.Context, sess domain.Session, id string, notify bool) (*domain.InvitationDelivery, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return nil, err
	}
	var cancel *domain.ITIPMessage
	var current domain.Event
	if notify && s.scheduling != nil {
		if current, err = s.calendar.Event(ctx, mb, id); err != nil {
			return nil, s.davError("no se pudo leer el evento", sess, err)
		}
		if s.organizes(sess, current) {
			msg, err := s.scheduling.EventInvitation(ctx, mb, id, domain.MethodCancel)
			if err != nil {
				return nil, s.davError("no se pudo escribir la cancelacion", sess, err)
			}
			cancel = &msg
		}
	}
	if err := s.calendar.DeleteEvent(ctx, mb, id); err != nil {
		return nil, s.davError("no se pudo borrar el evento", sess, err)
	}
	if cancel == nil {
		return nil, nil
	}
	return s.deliverInvitation(ctx, sess, *cancel, domain.InvitationCancel, summaryOf(current.EventInput)), nil
}
