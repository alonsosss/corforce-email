package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

const (
	// reminderItemTimeout acota un recordatorio de principio a fin (localizar, buscar respuesta en las
	// carpetas y mover o copiar); el arriendo lo supera para que nadie lo reclame mientras sigue en curso.
	reminderItemTimeout = 2 * time.Minute
	// maxReplyFolders acota las carpetas en las que un seguimiento busca respuesta: cada una es un
	// SELECT y un SEARCH.
	maxReplyFolders = 200
)

// Snooze pospone mensajes de folder hasta at: los mueve a la carpeta Snoozed (creandola si falta) y
// registra por cada uno su recordatorio en mail-directory, con la carpeta a la que vuelve. Un mensaje
// que ya no esta queda en Failed; un fallo del directorio devuelve el mensaje a su carpeta.
func (s *Service) Snooze(ctx context.Context, sess domain.Session, folder string, uids []uint32, at time.Time) (domain.SnoozeResult, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return domain.SnoozeResult{}, err
	}
	uids, err := domain.NormalizeUIDs(uids)
	if err != nil {
		return domain.SnoozeResult{}, err
	}
	if err := domain.ValidateReminderAt("until", at, s.clock(), s.cfg.MaxReminderDays); err != nil {
		return domain.SnoozeResult{}, err
	}
	res := domain.SnoozeResult{Snoozed: []domain.Reminder{}, Failed: []uint32{}}
	err = s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		src, ok := domain.FindFolder(folders, folder)
		if !ok {
			return domain.ErrFolderNotFound
		}
		if err := domain.CheckSnoozable(src); err != nil {
			return err
		}
		dest, err := ensureRoleFolder(ctx, mb, folders, domain.RoleSnoozed, domain.SnoozedFolderName)
		if err != nil {
			return err
		}
		for i, uid := range uids {
			r, err := s.snoozeOne(ctx, sess, mb, src.Name, dest, uid, at)
			switch {
			case err == nil:
				res.Snoozed = append(res.Snoozed, r)
			case errors.Is(err, domain.ErrMessageNotFound):
				res.Failed = append(res.Failed, uid)
			case len(res.Snoozed) == 0:
				return err
			default:
				s.logger.Warn("webmail: lote pospuesto a medias", zap.String("username", sess.Username), zap.Error(err))
				res.Failed = append(res.Failed, uids[i:]...)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return domain.SnoozeResult{}, s.reminderError("no se pudo posponer", sess, err)
	}
	s.logger.Info("webmail: mensajes pospuestos", zap.String("username", sess.Username), zap.Int("snoozed", len(res.Snoozed)),
		zap.Int("failed", len(res.Failed)), zap.Time("until", at))
	return res, nil
}

// snoozeOne mueve un mensaje a Snoozed y registra su fila. Un mismo mensaje con otro pospuesto activo
// (lo saco el usuario a mano y lo vuelve a posponer) cancela el anterior: su hora ya no vale.
func (s *Service) snoozeOne(ctx context.Context, sess domain.Session, mb ports.Mailbox, src, dest string, uid uint32, at time.Time) (domain.Reminder, error) {
	st, err := mb.Stat(ctx, src, uid)
	if err != nil {
		return domain.Reminder{}, err
	}
	messageID := st.MessageID
	if !domain.IsValidMessageID(messageID) {
		messageID = ""
	}
	moved, err := mb.MoveTracked(ctx, src, uid, dest)
	if err != nil {
		return domain.Reminder{}, err
	}
	if moved.UID == 0 && messageID != "" {
		if moved.UID, err = mb.FindByMessageID(ctx, dest, messageID); err == nil && moved.UID != 0 {
			var found domain.StoredMessage
			found, err = mb.Stat(ctx, dest, moved.UID)
			moved.UIDValidity = found.UIDValidity
		}
	}
	if err != nil || moved.UID == 0 || moved.UIDValidity == 0 {
		// Sin su referencia el mensaje no volveria nunca: se queda en Snoozed, donde el usuario lo ve.
		return domain.Reminder{}, unavailable(fmt.Errorf("el servidor IMAP no informo del UID del mensaje pospuesto: %v", err))
	}
	in := domain.NewReminder{
		Username: sess.Username, Kind: domain.ReminderSnooze, MessageID: messageID, Folder: dest,
		UIDValidity: moved.UIDValidity, UID: moved.UID, ReturnFolder: src, DueAt: at, Subject: st.Subject,
		Addresses: domain.ReminderAddresses([]string{st.From}),
	}
	r, err := s.reminders.CreateReminder(ctx, in)
	if errors.Is(err, domain.ErrReminderExists) {
		if err = s.cancelActive(ctx, sess, domain.ReminderSnooze, messageID); err == nil {
			r, err = s.reminders.CreateReminder(ctx, in)
		}
	}
	if err != nil {
		if _, merr := mb.MoveTracked(context.WithoutCancel(ctx), dest, moved.UID, src); merr != nil {
			s.logger.Error("webmail: un pospuesto sin fila no volvio a su carpeta", zap.String("username", sess.Username),
				zap.String("folder", src), zap.Error(merr))
		}
		return domain.Reminder{}, err
	}
	return r, nil
}

// cancelActive cancela los recordatorios activos de ese tipo y mensaje.
func (s *Service) cancelActive(ctx context.Context, sess domain.Session, kind domain.ReminderKind, messageID string) error {
	rows, err := s.reminders.ListReminders(ctx, sess.Username, kind)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.MessageID == messageID && r.Status == domain.ReminderPending {
			if err := s.reminders.CancelReminder(ctx, sess.Username, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureRoleFolder devuelve la carpeta con ese papel y la crea con su nombre si el buzon aun no la tiene.
func ensureRoleFolder(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, role domain.FolderRole, name string) (string, error) {
	if f, ok := domain.FolderWithRole(folders, role); ok {
		return f.Name, nil
	}
	err := mb.CreateFolder(ctx, name)
	if err != nil && !errors.Is(err, domain.ErrFolderExists) {
		return "", err
	}
	return name, nil
}

// ListReminders devuelve los recordatorios del buzon de la sesion de ese tipo.
func (s *Service) ListReminders(ctx context.Context, sess domain.Session, kind domain.ReminderKind) ([]domain.Reminder, error) {
	rows, err := s.reminders.ListReminders(ctx, sess.Username, kind)
	if err != nil {
		s.logFailure("no se pudieron leer los recordatorios", sess, err)
		return nil, unavailable(err)
	}
	return rows, nil
}

// RescheduleReminder cambia la hora de un recordatorio pendiente del buzon de la sesion.
func (s *Service) RescheduleReminder(ctx context.Context, sess domain.Session, id string, at time.Time) (domain.Reminder, error) {
	if err := domain.ValidateReminderID(id); err != nil {
		return domain.Reminder{}, err
	}
	if err := domain.ValidateReminderAt("until", at, s.clock(), s.cfg.MaxReminderDays); err != nil {
		return domain.Reminder{}, err
	}
	r, err := s.reminders.RescheduleReminder(ctx, sess.Username, id, at)
	if err != nil {
		return domain.Reminder{}, s.reminderError("no se pudo cambiar la hora del recordatorio", sess, err)
	}
	return r, nil
}

// CancelFollowUp cancela un seguimiento del buzon; el mensaje enviado no se toca.
func (s *Service) CancelFollowUp(ctx context.Context, sess domain.Session, id string) error {
	if err := domain.ValidateReminderID(id); err != nil {
		return err
	}
	if err := s.reminders.CancelReminder(ctx, sess.Username, id); err != nil {
		return s.reminderError("no se pudo cancelar el seguimiento", sess, err)
	}
	return nil
}

// Unsnooze devuelve ya un pospuesto a su carpeta. La fila se cancela antes de tocar el mensaje: desde
// ese momento el trabajador ya no la reclama. El mensaje vuelve tal cual, con sus marcas.
func (s *Service) Unsnooze(ctx context.Context, sess domain.Session, id string) error {
	if err := domain.ValidateReminderID(id); err != nil {
		return err
	}
	rows, err := s.reminders.ListReminders(ctx, sess.Username, domain.ReminderSnooze)
	if err != nil {
		return s.reminderError("no se pudieron leer los pospuestos", sess, err)
	}
	var row *domain.Reminder
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return domain.ErrReminderNotFound
	}
	if err := s.reminders.CancelReminder(ctx, sess.Username, id); err != nil {
		return s.reminderError("no se pudo cancelar el pospuesto", sess, err)
	}
	err = s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		uid, err := locateReminder(ctx, mb, folders, row.Folder, row.UIDValidity, row.UID, row.MessageID)
		if err != nil || uid == 0 {
			return err
		}
		_, err = mb.Move(ctx, row.Folder, []uint32{uid}, returnFolder(folders, row.ReturnFolder))
		return err
	})
	if err != nil {
		// La fila ya esta cancelada: el mensaje se queda en Snoozed, donde el usuario lo ve y lo mueve.
		s.logger.Warn("webmail: pospuesto cancelado sin devolver el mensaje", zap.String("username", sess.Username),
			zap.String("reminder_id", id), zap.Error(err))
	}
	return nil
}

// CheckFollowUp valida un seguimiento antes de enviar: un plazo que no vale se rechaza sin que el
// mensaje salga. base es la hora de salida de un envio programado; cero, un envio inmediato.
func (s *Service) CheckFollowUp(days int, base time.Time) error {
	if err := domain.ValidateFollowUpDays(days, s.cfg.MaxReminderDays); err != nil {
		return err
	}
	return domain.ValidateReminderAt("follow_up_days", s.followUpDue(days, base), s.clock(), s.cfg.MaxReminderDays)
}

func (s *Service) followUpDue(days int, base time.Time) time.Time {
	if base.IsZero() {
		base = s.clock()
	}
	return base.Add(time.Duration(days) * 24 * time.Hour)
}

// FollowUpRequest pide seguimiento de un mensaje recien enviado o programado: si nadie responde en Days
// dias desde Base (la hora de salida; cero es ahora), el mensaje vuelve a INBOX marcado.
type FollowUpRequest struct {
	MessageID  string
	Subject    string
	Recipients []string
	Base       time.Time
	Days       int
}

// CreateFollowUp registra el seguimiento. El mensaje se referencia en Enviados si ya esta (envio
// normal); uno programado aun no tiene copia y el trabajador lo busca por su Message-ID. Repetir la
// peticion (el reintento de un envio) devuelve el seguimiento ya activo.
func (s *Service) CreateFollowUp(ctx context.Context, sess domain.Session, req FollowUpRequest) (domain.Reminder, error) {
	if err := s.CheckFollowUp(req.Days, req.Base); err != nil {
		return domain.Reminder{}, err
	}
	if !domain.IsValidMessageID(req.MessageID) {
		return domain.Reminder{}, domain.NewValidationError("message_id", "no es un Message-ID válido")
	}
	due := s.followUpDue(req.Days, req.Base)
	in := domain.NewReminder{
		Username: sess.Username, Kind: domain.ReminderFollowUp, MessageID: req.MessageID, DueAt: due,
		Subject: req.Subject, Addresses: domain.ReminderAddresses(req.Recipients),
	}
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		sent, ok := domain.FolderWithRole(folders, domain.RoleSent)
		if !ok {
			return errors.New("el buzón no tiene carpeta de enviados")
		}
		in.Folder = sent.Name
		uid, err := mb.FindByMessageID(ctx, sent.Name, req.MessageID)
		if err != nil || uid == 0 {
			return err
		}
		st, err := mb.Stat(ctx, sent.Name, uid)
		if err != nil {
			return err
		}
		in.UID, in.UIDValidity = uid, st.UIDValidity
		return nil
	})
	if err != nil {
		return domain.Reminder{}, s.reminderError("no se pudo localizar el mensaje del seguimiento", sess, err)
	}
	r, err := s.reminders.CreateReminder(ctx, in)
	if errors.Is(err, domain.ErrReminderExists) {
		return s.activeReminder(ctx, sess, domain.ReminderFollowUp, req.MessageID)
	}
	if err != nil {
		return domain.Reminder{}, s.reminderError("no se pudo registrar el seguimiento", sess, err)
	}
	s.logger.Info("webmail: seguimiento registrado", zap.String("username", sess.Username), zap.String("reminder_id", r.ID),
		zap.Time("due_at", r.DueAt))
	return r, nil
}

func (s *Service) activeReminder(ctx context.Context, sess domain.Session, kind domain.ReminderKind, messageID string) (domain.Reminder, error) {
	rows, err := s.reminders.ListReminders(ctx, sess.Username, kind)
	if err != nil {
		return domain.Reminder{}, s.reminderError("no se pudieron leer los recordatorios", sess, err)
	}
	for _, r := range rows {
		if r.MessageID == messageID && (r.Status == domain.ReminderPending || r.Status == domain.ReminderRunning) {
			return r, nil
		}
	}
	return domain.Reminder{}, domain.ErrReminderExists
}

func (s *Service) reminderError(msg string, sess domain.Session, err error) error {
	var verr *domain.ValidationError
	if errors.As(err, &verr) || errors.Is(err, domain.ErrReminderNotFound) || errors.Is(err, domain.ErrReminderNotPending) ||
		errors.Is(err, domain.ErrReminderLimit) || errors.Is(err, domain.ErrReminderExists) ||
		errors.Is(err, domain.ErrFolderNotFound) || errors.Is(err, domain.ErrMessageNotFound) {
		return err
	}
	s.logFailure(msg, sess, err)
	return unavailable(err)
}

// returnFolder es la carpeta registrada si sigue existiendo y, si no, la bandeja de entrada.
func returnFolder(folders []domain.Folder, registered string) string {
	if f, ok := domain.FindFolder(folders, registered); ok && f.Selectable {
		return f.Name
	}
	if f, ok := domain.FolderWithRole(folders, domain.RoleInbox); ok {
		return f.Name
	}
	return "INBOX"
}

// locateReminder encuentra el mensaje de una fila en su carpeta: por su UID si la carpeta conserva la
// UIDVALIDITY y el Message-ID coincide y, si no, por el Message-ID. 0 si ya no esta.
func locateReminder(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, folder string, uidValidity, uid uint32, messageID string) (uint32, error) {
	if _, ok := domain.FindFolder(folders, folder); !ok {
		return 0, nil
	}
	if uid != 0 {
		msg, err := mb.Stat(ctx, folder, uid)
		switch {
		case err == nil:
			if msg.UIDValidity == uidValidity && msg.MessageID == messageID {
				return uid, nil
			}
		case errors.Is(err, domain.ErrMessageNotFound):
		default:
			return 0, err
		}
	}
	if !domain.IsValidMessageID(messageID) {
		return 0, nil
	}
	return mb.FindByMessageID(ctx, folder, messageID)
}

// RunReminders es el trabajador de recordatorios, como RunScheduledSends: cada intervalo reclama los
// vencidos de la celda y los aplica; si el lote vino lleno vuelve a reclamar sin esperar.
func (s *Service) RunReminders(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.ReminderPollInterval)
	defer ticker.Stop()
	for {
		for s.processReminderBatch(ctx) == s.cfg.ReminderBatch && ctx.Err() == nil {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) processReminderBatch(ctx context.Context) int {
	claims, err := s.reminders.ClaimReminders(ctx, s.cfg.ReminderBatch, reminderItemTimeout+pendingMargin)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("webmail: no se pudieron reclamar los recordatorios", zap.Error(err))
		}
		return 0
	}
	var wg sync.WaitGroup
	for _, c := range claims {
		wg.Add(1)
		go func(c domain.ReminderClaim) {
			defer wg.Done()
			s.runReminder(ctx, c)
		}(c)
	}
	wg.Wait()
	return len(claims)
}

func (s *Service) runReminder(ctx context.Context, c domain.ReminderClaim) {
	itemCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reminderItemTimeout)
	defer cancel()
	outcome := s.applyReminder(itemCtx, c)
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
	defer cancelFinish()
	if err := s.reminders.FinishReminder(finishCtx, c.ID, outcome); err != nil {
		// Se vuelve a reclamar al vencer el arriendo; aplicarla otra vez no duplica nada (ver applyReminder).
		s.logger.Error("webmail: no se pudo cerrar el recordatorio", zap.String("reminder_id", c.ID),
			zap.String("status", string(outcome.Status)), zap.Error(err))
		return
	}
	s.logger.Info("webmail: recordatorio cerrado", zap.String("reminder_id", c.ID), zap.String("username", c.Username),
		zap.String("kind", string(c.Kind)), zap.String("status", string(outcome.Status)), zap.String("result", string(outcome.Result)),
		zap.String("error", outcome.Error))
}

func reminderFailed(reason string, retry bool) domain.ReminderOutcome {
	return domain.ReminderOutcome{Status: domain.ReminderFailed, Error: reason, Retry: retry}
}

func reminderDone(result domain.ReminderResult) domain.ReminderOutcome {
	return domain.ReminderOutcome{Status: domain.ReminderDone, Result: result}
}

// applyReminder aplica una fila reclamada y dice como cerrarla. Es idempotente sin registro propio:
// un pospuesto ya devuelto no esta en Snoozed (missing) y un seguimiento ya avisado encuentra su copia
// en INBOX. Un fallo del buzon se reintenta; una fila ilegible, no.
func (s *Service) applyReminder(ctx context.Context, c domain.ReminderClaim) domain.ReminderOutcome {
	username, ok := domain.NormalizeUsername(c.Username)
	if !ok {
		return reminderFailed("fila inválida", false)
	}
	sess := domain.Session{Username: username}
	var outcome domain.ReminderOutcome
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		switch c.Kind {
		case domain.ReminderSnooze:
			outcome, err = s.returnSnoozed(ctx, mb, folders, c)
		case domain.ReminderFollowUp:
			outcome, err = s.checkFollowUp(ctx, mb, folders, c)
		default:
			outcome = reminderFailed("tipo de recordatorio desconocido", false)
		}
		return err
	})
	if errors.Is(err, domain.ErrMessageTooLarge) {
		return reminderFailed(domain.ErrMessageTooLarge.Error(), false)
	}
	if err != nil {
		s.logger.Warn("webmail: recordatorio sin aplicar", zap.String("reminder_id", c.ID), zap.String("username", username), zap.Error(err))
		return reminderFailed("buzón no disponible", true)
	}
	return outcome
}

// returnSnoozed devuelve un pospuesto a su carpeta sin \Seen. Si ya no esta en Snoozed (el usuario lo
// movio o lo borro) no toca nada.
func (s *Service) returnSnoozed(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, c domain.ReminderClaim) (domain.ReminderOutcome, error) {
	uid, err := locateReminder(ctx, mb, folders, c.Folder, c.UIDValidity, c.UID, c.MessageID)
	if err != nil {
		return domain.ReminderOutcome{}, err
	}
	if uid == 0 {
		return reminderDone(domain.ReminderMissing), nil
	}
	if _, err := mb.SetFlags(ctx, c.Folder, []uint32{uid}, domain.FlagChange{Remove: []domain.Flag{domain.FlagSeen}}); err != nil {
		return domain.ReminderOutcome{}, err
	}
	if _, err := mb.Move(ctx, c.Folder, []uint32{uid}, returnFolder(folders, c.ReturnFolder)); err != nil {
		return domain.ReminderOutcome{}, err
	}
	return reminderDone(domain.ReminderReturned), nil
}

// checkFollowUp busca respuesta al mensaje enviado y, si no la hay, deja una copia en INBOX con
// \Flagged y sin \Seen (el aviso de la bandeja la anuncia). Una copia ya presente (un intento anterior
// que no llego a cerrar la fila) solo se vuelve a marcar.
func (s *Service) checkFollowUp(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, c domain.ReminderClaim) (domain.ReminderOutcome, error) {
	if !domain.IsValidMessageID(c.MessageID) {
		return reminderFailed("fila sin Message-ID", false), nil
	}
	sentFolder, uid, err := locateSent(ctx, mb, folders, c)
	if err != nil {
		return domain.ReminderOutcome{}, err
	}
	if uid == 0 {
		return reminderDone(domain.ReminderMissing), nil
	}
	replied, err := hasReply(ctx, mb, folders, c.MessageID)
	if err != nil {
		return domain.ReminderOutcome{}, err
	}
	if replied {
		return reminderDone(domain.ReminderReplied), nil
	}
	inbox := returnFolder(folders, "INBOX")
	flag := domain.FlagChange{Add: []domain.Flag{domain.FlagFlagged}, Remove: []domain.Flag{domain.FlagSeen}}
	existing, err := mb.FindByMessageID(ctx, inbox, c.MessageID)
	if err != nil {
		return domain.ReminderOutcome{}, err
	}
	if existing != 0 {
		_, err = mb.SetFlags(ctx, inbox, []uint32{existing}, flag)
		return reminderDone(domain.ReminderReminded), err
	}
	_, body, err := mb.OpenRaw(ctx, sentFolder, uid, s.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return domain.ReminderOutcome{}, err
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return domain.ReminderOutcome{}, err
	}
	if _, err := mb.Append(ctx, inbox, raw, []domain.Flag{domain.FlagFlagged}, s.clock()); err != nil {
		return domain.ReminderOutcome{}, err
	}
	return reminderDone(domain.ReminderReminded), nil
}

// locateSent encuentra el mensaje enviado: en la carpeta registrada y, si no esta (un envio programado
// que salio despues de pedir el seguimiento), en la de enviados por su Message-ID.
func locateSent(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, c domain.ReminderClaim) (string, uint32, error) {
	uid, err := locateReminder(ctx, mb, folders, c.Folder, c.UIDValidity, c.UID, c.MessageID)
	if err != nil || uid != 0 {
		return c.Folder, uid, err
	}
	sent, ok := domain.FolderWithRole(folders, domain.RoleSent)
	if !ok || sent.Name == c.Folder {
		return "", 0, nil
	}
	uid, err = mb.FindByMessageID(ctx, sent.Name, c.MessageID)
	return sent.Name, uid, err
}

// hasReply busca una respuesta en las carpetas del buzon salvo las de lo propio (enviados, borradores y
// programados), donde una respuesta del usuario al mismo hilo no cuenta. Una respuesta archivada,
// borrada o marcada como spam si cuenta: llego.
func hasReply(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, messageID string) (bool, error) {
	searched := 0
	for _, f := range folders {
		if !f.Selectable || f.Role == domain.RoleSent || f.Role == domain.RoleDrafts || f.Role == domain.RoleScheduled {
			continue
		}
		if searched == maxReplyFolders {
			break
		}
		searched++
		found, err := mb.HasReply(ctx, f.Name, messageID)
		if errors.Is(err, domain.ErrFolderNotFound) {
			continue
		}
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}
