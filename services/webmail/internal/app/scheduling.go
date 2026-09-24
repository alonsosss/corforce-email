package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// Planificacion (docs/Plan_Webmail_Innovador.md, bloque C3): el calendario, las invitaciones y las citas viven en
// mail-dav; el webmail pone la identidad de la sesion, lee los text/calendar del buzon y envia los correos iMIP
// desde el buzon por el submission de la celda, como cualquier otro correo.

// bookingProbe es la ventana con la que se comprueba, antes de reservar, que la pagina existe y de quien es.
const bookingProbe = time.Minute

func (s *Service) schedulingReady() error {
	if s.scheduling == nil || s.invitations == nil {
		return unavailable(errors.New("planificacion sin cablear"))
	}
	return nil
}

// asOrganizer pone como organizador de un evento con invitados al buzon de la sesion; sin invitados no hay
// organizador. Lo que diga el cliente no cuenta.
func (s *Service) asOrganizer(sess domain.Session, in *domain.EventInput) {
	in.Organizer = nil
	if len(in.Attendees) > 0 {
		in.Organizer = &domain.Party{Email: strings.ToLower(sess.Username), Name: sess.DisplayName}
	}
}

// organizes dice si el buzon de la sesion organiza el evento con invitados: solo entonces se envian invitaciones.
func (s *Service) organizes(sess domain.Session, e domain.Event) bool {
	return e.Organizer != nil && len(e.Attendees) > 0 && strings.EqualFold(e.Organizer.Email, sess.Username)
}

func summaryOf(in domain.EventInput) domain.InvitationSummary {
	out := domain.InvitationSummary{Title: in.Title, Start: in.Start, End: in.End, AllDay: in.AllDay, TimeZone: in.TimeZone, Location: in.Location}
	if o := in.Organizer; o != nil {
		out.Organizer = o.Email
		if o.Name != "" {
			out.Organizer = o.Name + " <" + o.Email + ">"
		}
	}
	return out
}

// notifyChange envia la invitacion (REQUEST) de un evento que el buzon organiza, tras guardarlo.
func (s *Service) notifyChange(ctx context.Context, sess domain.Session, mb domain.MailboxRef, e domain.Event, notify bool) domain.SavedEvent {
	out := domain.SavedEvent{Event: e}
	if !notify || s.scheduling == nil || !s.organizes(sess, e) {
		return out
	}
	msg, err := s.scheduling.EventInvitation(ctx, mb, e.ID, domain.MethodRequest)
	if err != nil {
		s.logFailure("no se pudo escribir la invitacion", sess, err)
		out.Delivery = &domain.InvitationDelivery{Method: domain.MethodRequest, Recipients: len(e.Attendees)}
		return out
	}
	out.Delivery = s.deliverInvitation(ctx, sess, msg, domain.InvitationRequest, summaryOf(e.EventInput))
	return out
}

// sendCalendarMail compone y entrega un correo iMIP desde from (una direccion que el buzon puede usar).
func (s *Service) sendCalendarMail(ctx context.Context, username string, m domain.InvitationMail) error {
	if s.invitations == nil {
		return unavailable(errors.New("compositor de invitaciones sin cablear"))
	}
	recipients := m.Recipients()
	if len(recipients) == 0 {
		return domain.NewValidationError("to", "hace falta al menos un destinatario")
	}
	if len(recipients) > s.cfg.Limits.MaxRecipients {
		return domain.ErrTooManyRecipients
	}
	m.MessageID, m.Date = newMessageID(m.From.Email), s.clock()
	raw, err := s.invitations.ComposeInvitation(m)
	if err != nil {
		return unavailable(err)
	}
	if int64(len(raw)) > s.cfg.Limits.MaxMessageBytes {
		return domain.ErrMessageTooLarge
	}
	return s.sender.Send(ctx, username, m.From.Email, recipients, raw)
}

func addressesOf(list []string) []domain.Address {
	out := make([]domain.Address, 0, len(list))
	for _, r := range list {
		if a, err := domain.NewAddress("to", "", r); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// deliverInvitation envia una invitacion o una cancelacion desde el buzon de la sesion (el organizador) a sus
// invitados. Un fallo no deshace el cambio del calendario: se registra y la interfaz lo ve en Sent.
func (s *Service) deliverInvitation(ctx context.Context, sess domain.Session, msg domain.ITIPMessage, kind domain.InvitationKind, summary domain.InvitationSummary) *domain.InvitationDelivery {
	to := addressesOf(msg.Recipients)
	out := &domain.InvitationDelivery{Method: msg.Method, Recipients: len(to)}
	if len(to) == 0 {
		return out
	}
	err := s.sendCalendarMail(ctx, sess.Username, domain.InvitationMail{
		From: domain.Address{Name: sess.DisplayName, Email: sess.Username}, To: to,
		Subject: domain.InvitationSubject(kind, summary.Title), Text: domain.InvitationText(kind, summary),
		Method: msg.Method, ICal: msg.ICal,
	})
	if err != nil {
		s.logger.Warn("webmail: no se pudo enviar la invitacion", zap.String("username", sess.Username), zap.String("method", msg.Method),
			zap.Int("recipients", len(to)), zap.Error(err))
		return out
	}
	out.Sent = true
	s.logger.Info("webmail: invitacion enviada", zap.String("username", sess.Username), zap.String("method", msg.Method), zap.Int("recipients", len(to)))
	return out
}

// UpdateOccurrence cambia una sola aparicion de una serie y, si el buzon la organiza con invitados, envia la
// invitacion actualizada.
func (s *Service) UpdateOccurrence(ctx context.Context, sess domain.Session, id, recurrenceID string, in domain.EventInput, ifMatch string, notify bool) (domain.SavedEvent, error) {
	mb, err := s.occurrenceTarget(sess, id, recurrenceID, ifMatch)
	if err != nil {
		return domain.SavedEvent{}, err
	}
	in.Recurrence, in.Attendees, in.Organizer = nil, nil, nil
	e, err := s.scheduling.UpdateOccurrence(ctx, mb, id, recurrenceID, in, ifMatch)
	if err != nil {
		return domain.SavedEvent{}, s.davError("no se pudo guardar la aparicion", sess, err)
	}
	return s.notifyChange(ctx, sess, mb, e, notify), nil
}

// DeleteOccurrence borra una sola aparicion de una serie; con invitados, la invitacion actualizada la retira de
// sus calendarios.
func (s *Service) DeleteOccurrence(ctx context.Context, sess domain.Session, id, recurrenceID, ifMatch string, notify bool) (domain.SavedEvent, error) {
	mb, err := s.occurrenceTarget(sess, id, recurrenceID, ifMatch)
	if err != nil {
		return domain.SavedEvent{}, err
	}
	e, err := s.scheduling.DeleteOccurrence(ctx, mb, id, recurrenceID, ifMatch)
	if err != nil {
		return domain.SavedEvent{}, s.davError("no se pudo borrar la aparicion", sess, err)
	}
	return s.notifyChange(ctx, sess, mb, e, notify), nil
}

func (s *Service) occurrenceTarget(sess domain.Session, id, recurrenceID, ifMatch string) (domain.MailboxRef, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return mb, err
	}
	if err := s.schedulingReady(); err != nil {
		return mb, err
	}
	if err := domain.ValidateResourceID(id); err != nil {
		return mb, err
	}
	if _, err := time.Parse(time.RFC3339, recurrenceID); err != nil {
		return mb, domain.NewValidationError("recurrence_id", "debe ser una fecha y hora RFC 3339")
	}
	return mb, domain.ValidateIfMatch(ifMatch)
}

// myAddresses son las direcciones del buzon: la suya y las que el directorio le deja usar. Sin directorio, la suya.
func (s *Service) myAddresses(ctx context.Context, sess domain.Session) []string {
	out := []string{strings.ToLower(sess.Username)}
	ids, err := s.directory.SenderIdentities(ctx, sess.Username)
	if err != nil {
		s.logger.Warn("webmail: sin remitentes del directorio; se usa solo la direccion del buzon", zap.String("username", sess.Username), zap.Error(err))
		return out
	}
	for _, a := range ids {
		if a = strings.ToLower(a); !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// calendarPart lee del mensaje su iCalendar (la primera parte text/calendar o, si no hay, application/ics) y su
// remitente. La parte se lee acotada como un cuerpo; su validacion es de mail-dav.
func (s *Service) calendarPart(ctx context.Context, sess domain.Session, folder string, uid uint32) (string, string, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return "", "", err
	}
	var ical, from string
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		raw, err := mb.Read(ctx, folder, uid, domain.ReadOptions{MaxBodyBytes: s.cfg.MaxBodyPartBytes})
		if err != nil {
			return err
		}
		if len(raw.Envelope.From) > 0 {
			from = raw.Envelope.From[0].Email
		}
		var part *domain.Part
		for i, p := range raw.Parts {
			if domain.IsCalendarPart(p.ContentType) && (part == nil || strings.EqualFold(p.ContentType, "text/calendar") && !strings.EqualFold(part.ContentType, "text/calendar")) {
				part = &raw.Parts[i]
			}
		}
		if part == nil {
			return domain.ErrPartNotFound
		}
		_, body, err := mb.OpenPart(ctx, folder, uid, part.ID, s.cfg.MaxBodyPartBytes)
		if err != nil {
			return err
		}
		defer body.Close()
		data, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		ical = string(data)
		return nil
	})
	return ical, from, err
}

// Invitation muestra la invitacion de un mensaje del buzon cruzada con su calendario.
func (s *Service) Invitation(ctx context.Context, sess domain.Session, folder string, uid uint32) (domain.Invitation, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.Invitation{}, err
	}
	if err := s.schedulingReady(); err != nil {
		return domain.Invitation{}, err
	}
	ical, _, err := s.calendarPart(ctx, sess, folder, uid)
	if err != nil {
		return domain.Invitation{}, err
	}
	inv, err := s.scheduling.InspectInvitation(ctx, mb, ical, s.myAddresses(ctx, sess))
	if err != nil {
		return domain.Invitation{}, s.davError("no se pudo leer la invitacion", sess, err)
	}
	return inv, nil
}

// RespondInvitation acepta, deja en tentativo o rechaza la invitacion de un mensaje: mail-dav la guarda (o la quita)
// en el calendario del buzon y escribe el REPLY, que sale hacia el organizador desde la direccion invitada.
func (s *Service) RespondInvitation(ctx context.Context, sess domain.Session, folder string, uid uint32, response string) (domain.InvitationResult, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.InvitationResult{}, err
	}
	if err := s.schedulingReady(); err != nil {
		return domain.InvitationResult{}, err
	}
	response, err = domain.ValidateInvitationResponse(response)
	if err != nil {
		return domain.InvitationResult{}, err
	}
	ical, _, err := s.calendarPart(ctx, sess, folder, uid)
	if err != nil {
		return domain.InvitationResult{}, err
	}
	addresses := s.myAddresses(ctx, sess)
	inv, err := s.scheduling.InspectInvitation(ctx, mb, ical, addresses)
	if err != nil {
		return domain.InvitationResult{}, s.davError("no se pudo leer la invitacion", sess, err)
	}
	ans, err := s.scheduling.RespondInvitation(ctx, mb, ical, addresses, response)
	if err != nil {
		return domain.InvitationResult{}, s.davError("no se pudo responder la invitacion", sess, err)
	}
	out := domain.InvitationResult{EventID: ans.EventID}
	organizer, err := domain.NewAddress("to", ans.Organizer.Name, ans.Organizer.Email)
	if err != nil {
		return out, nil
	}
	from := domain.Address{Email: ans.Attendee}
	if err := s.checkSender(ctx, sess, &from); err != nil {
		s.logFailure("la direccion invitada no es un remitente del buzon", sess, err)
		return out, nil
	}
	if strings.EqualFold(from.Email, sess.Username) {
		from.Name = sess.DisplayName
	}
	summary := domain.InvitationSummary{Title: inv.Title, AllDay: inv.AllDay, TimeZone: inv.TimeZone, Location: inv.Location, Attendee: from.Email}
	if inv.Start != nil && inv.End != nil {
		summary.Start, summary.End = *inv.Start, *inv.End
	}
	kind := domain.ReplyKind(response)
	err = s.sendCalendarMail(ctx, sess.Username, domain.InvitationMail{
		From: from, To: []domain.Address{organizer}, Subject: domain.InvitationSubject(kind, inv.Title),
		Text: domain.InvitationText(kind, summary), Method: domain.MethodReply, ICal: ans.Reply,
	})
	if err != nil {
		s.logger.Warn("webmail: no se pudo enviar la respuesta a la invitacion", zap.String("username", sess.Username), zap.Error(err))
		return out, nil
	}
	out.ReplySent = true
	return out, nil
}

// ApplyInvitation aplica al calendario del buzon la respuesta (REPLY) o la cancelacion (CANCEL) de un mensaje: la
// respuesta solo cuenta si la envia el propio invitado y la cancelacion si la envia el organizador (el remitente
// del mensaje, que el escudo antifraude contrasta con SPF, DKIM y DMARC al leerlo).
func (s *Service) ApplyInvitation(ctx context.Context, sess domain.Session, folder string, uid uint32) (domain.InvitationApplied, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.InvitationApplied{}, err
	}
	if err := s.schedulingReady(); err != nil {
		return domain.InvitationApplied{}, err
	}
	ical, from, err := s.calendarPart(ctx, sess, folder, uid)
	if err != nil {
		return domain.InvitationApplied{}, err
	}
	if from == "" {
		return domain.InvitationApplied{}, domain.NewValidationError("from", "el mensaje no tiene remitente")
	}
	res, err := s.scheduling.ApplyInvitation(ctx, mb, ical, from, s.myAddresses(ctx, sess))
	if err != nil {
		return domain.InvitationApplied{}, s.davError("no se pudo aplicar la invitacion", sess, err)
	}
	return res, nil
}

// Availability devuelve la ocupacion (solo inicio y fin) de companeros de la empresa.
func (s *Service) Availability(ctx context.Context, sess domain.Session, raw []string, w domain.EventWindow) ([]domain.MailboxAvailability, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return nil, err
	}
	if err := s.schedulingReady(); err != nil {
		return nil, err
	}
	addresses, err := domain.NewAvailabilityQuery(raw)
	if err != nil {
		return nil, err
	}
	out, err := s.scheduling.Availability(ctx, mb, addresses, w)
	if err != nil {
		return nil, s.davError("no se pudo leer la disponibilidad", sess, err)
	}
	return out, nil
}

func (s *Service) withLink(sess domain.Session, p domain.BookingPage) domain.BookingPage {
	p.Cell, p.TenantID = s.cfg.CellCode, sess.TenantID
	return p
}

// BookingSettings devuelve la pagina de citas del buzon con los datos de su enlace.
func (s *Service) BookingSettings(ctx context.Context, sess domain.Session) (domain.BookingPage, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.BookingPage{}, err
	}
	if err := s.schedulingReady(); err != nil {
		return domain.BookingPage{}, err
	}
	p, err := s.scheduling.BookingSettings(ctx, mb)
	if err != nil {
		return domain.BookingPage{}, s.davError("no se pudo leer la pagina de citas", sess, err)
	}
	return s.withLink(sess, p), nil
}

// SaveBookingSettings guarda la pagina de citas; el dueno es el buzon de la sesion con su nombre.
func (s *Service) SaveBookingSettings(ctx context.Context, sess domain.Session, in domain.BookingSettings, regenerate bool) (domain.BookingPage, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.BookingPage{}, err
	}
	if err := s.schedulingReady(); err != nil {
		return domain.BookingPage{}, err
	}
	p, err := s.scheduling.SaveBookingSettings(ctx, mb, in, sess.DisplayName, regenerate)
	if err != nil {
		return domain.BookingPage{}, s.davError("no se pudo guardar la pagina de citas", sess, err)
	}
	return s.withLink(sess, p), nil
}

// publicTarget comprueba la forma de una ruta publica y que su celda sea la de esta instancia: el gateway enruta
// por ese segmento sin verificarlo, y una celda cambiada llega aqui como cualquier enlace que no existe.
func (s *Service) publicTarget(cell, tenant, page string) error {
	if !domain.ValidPublicBookingTarget(cell, tenant, page) || cell != s.cfg.CellCode {
		return domain.ErrResourceNotFound
	}
	return s.schedulingReady()
}

func (s *Service) publicError(msg string, err error) error {
	var rejection *domain.ServiceRejection
	if errors.As(err, &rejection) && rejection.Kind != domain.RejectUnavailable {
		return err
	}
	s.logger.Warn("webmail: "+msg, zap.Error(err))
	return unavailable(err)
}

// PublicBooking es la pagina publica de citas con sus huecos libres; nunca lleva la direccion del dueno.
func (s *Service) PublicBooking(ctx context.Context, cell, tenant, page string, w domain.EventWindow) (domain.PublicBookingPage, error) {
	if err := s.publicTarget(cell, tenant, page); err != nil {
		return domain.PublicBookingPage{}, err
	}
	p, err := s.scheduling.PublicBooking(ctx, tenant, page, w)
	if err != nil {
		return domain.PublicBookingPage{}, s.publicError("no se pudo leer la pagina de citas", err)
	}
	p.OwnerAddress = ""
	return p, nil
}

// Book reserva una cita desde la pagina publica: comprueba que el dueno sea un buzon de esta celda (de otra forma
// no se podria enviar desde el), la reserva en mail-dav y envia desde el buzon del dueno la invitacion al
// visitante con copia al dueno, que es tambien la confirmacion. El texto del correo es del servidor: nada de lo que
// escribe el visitante sale en el. Una peticion que rellena la trampa para robots no reserva nada y recibe la misma
// respuesta que una correcta sin datos.
func (s *Service) Book(ctx context.Context, cell, tenant, page string, req domain.BookingRequest) (domain.BookingResult, error) {
	if err := s.publicTarget(cell, tenant, page); err != nil {
		return domain.BookingResult{}, err
	}
	if strings.TrimSpace(req.Website) != "" {
		s.logger.Info("webmail: reserva descartada por la trampa para robots", zap.String("tenant_id", tenant))
		return domain.BookingResult{}, nil
	}
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(req.Start))
	if err != nil {
		return domain.BookingResult{}, domain.NewValidationError("start", "debe ser una fecha y hora RFC 3339")
	}
	info, err := s.scheduling.PublicBooking(ctx, tenant, page, domain.EventWindow{Start: start, End: start.Add(bookingProbe)})
	if err != nil {
		return domain.BookingResult{}, s.publicError("no se pudo leer la pagina de citas", err)
	}
	owner, err := domain.NewAddress("from", info.OwnerName, info.OwnerAddress)
	if err != nil {
		return domain.BookingResult{}, domain.ErrResourceNotFound
	}
	ids, err := s.directory.SenderIdentities(ctx, owner.Email)
	if err != nil {
		return domain.BookingResult{}, s.publicError("no se pudo comprobar el dueno de la pagina de citas", err)
	}
	if !slices.Contains(ids, strings.ToLower(owner.Email)) {
		s.logger.Warn("webmail: pagina de citas de un buzon que no es de esta celda", zap.String("tenant_id", tenant))
		return domain.BookingResult{}, domain.ErrResourceNotFound
	}
	conf, err := s.scheduling.Book(ctx, tenant, page, req)
	if err != nil {
		return domain.BookingResult{}, s.publicError("no se pudo reservar la cita", err)
	}
	out := domain.BookingResult{Title: conf.Title, Start: conf.Start, End: conf.End, TimeZone: conf.TimeZone, OwnerName: conf.Owner.Name}
	to := addressesOf(conf.Invitation.Recipients)
	summary := domain.InvitationSummary{Title: conf.Title, Start: conf.Start, End: conf.End, TimeZone: conf.TimeZone, Organizer: owner.Email}
	if owner.Name != "" {
		summary.Organizer = owner.Name + " <" + owner.Email + ">"
	}
	err = s.sendCalendarMail(ctx, owner.Email, domain.InvitationMail{
		From: owner, To: to, Cc: []domain.Address{owner}, Subject: domain.InvitationSubject(domain.InvitationBooking, conf.Title),
		Text: domain.InvitationText(domain.InvitationBooking, summary), Method: conf.Invitation.Method, ICal: conf.Invitation.ICal,
	})
	if err != nil {
		s.logger.Warn("webmail: cita reservada sin confirmacion enviada", zap.String("tenant_id", tenant), zap.String("event_id", conf.EventID), zap.Error(err))
		return out, nil
	}
	out.ConfirmationSent = true
	return out, nil
}
