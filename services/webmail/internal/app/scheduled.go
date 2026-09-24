package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// scheduledFlags marcan el mensaje que espera en Scheduled: leido y borrador, de modo que al
// cancelarlo vuelva a Borradores como un borrador mas.
var scheduledFlags = []domain.Flag{domain.FlagSeen, domain.FlagDraft}

// Schedule programa el envio del borrador para at. Compone y comprueba el mensaje igual que Send
// (validaciones, remitente permitido, adjuntos del buzon y ClamAV), lo guarda en la carpeta
// Scheduled (creandola si falta) y registra la fila en mail-directory, que es quien la recuerda.
//
// Idempotencia: la misma clave que Send, con la hora dentro de la huella. Repetir la peticion
// devuelve la fila ya creada sin programar otra.
func (s *Service) Schedule(ctx context.Context, sess domain.Session, d domain.Draft, at time.Time, opts domain.SendOptions) (domain.ScheduledResult, error) {
	if err := domain.ValidateIdempotencyKey(opts.IdempotencyKey); err != nil {
		return domain.ScheduledResult{}, err
	}
	if err := domain.ValidateSendAt(at, s.clock(), s.cfg.MaxScheduledDays); err != nil {
		return domain.ScheduledResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer cancel()

	key := sendKey(sess.Username, opts.IdempotencyKey)
	fp := scheduleFingerprint(d, opts.ReplaceUID, at)
	rec, reserved, err := s.ledger.Reserve(ctx, key, domain.SendRecord{State: domain.SendPending, Fingerprint: fp}, s.cfg.SendTimeout+pendingMargin)
	if err != nil {
		s.logger.Error("webmail: no se pudo reservar la clave del envio programado", zap.String("username", sess.Username), zap.Error(err))
		return domain.ScheduledResult{}, unavailable(err)
	}
	if !reserved {
		return s.replaySchedule(sess, rec, fp, at)
	}

	out, _, stored, err := s.build(ctx, sess, d)
	if err != nil {
		s.release(ctx, key, rec.Token)
		return domain.ScheduledResult{}, err
	}
	id, err := s.storeScheduled(ctx, sess, out, stored, at, opts.ReplaceUID)
	if err != nil {
		s.release(ctx, key, rec.Token)
		return domain.ScheduledResult{}, err
	}
	rec.State, rec.MessageID, rec.ScheduledID = domain.SendScheduled, out.MessageID, id
	s.record(ctx, key, rec)
	s.logger.Info("webmail: envio programado", zap.String("username", sess.Username), zap.String("scheduled_id", id),
		zap.String("message_id", out.MessageID), zap.Time("send_at", at))
	return domain.ScheduledResult{ID: id, SendAt: at}, nil
}

func (s *Service) replaySchedule(sess domain.Session, rec domain.SendRecord, fp string, at time.Time) (domain.ScheduledResult, error) {
	if rec.Fingerprint != fp {
		s.logger.Warn("webmail: clave de envio repetida con otro mensaje", zap.String("username", sess.Username))
		return domain.ScheduledResult{}, domain.ErrIdempotencyKeyReused
	}
	switch rec.State {
	case domain.SendPending:
		return domain.ScheduledResult{}, domain.ErrSendInProgress
	case domain.SendScheduled:
		return domain.ScheduledResult{ID: rec.ScheduledID, SendAt: at, Replayed: true}, nil
	default:
		return domain.ScheduledResult{}, unavailable(fmt.Errorf("estado de envio programado desconocido %q", rec.State))
	}
}

// storeScheduled guarda el mensaje en Scheduled y registra la fila. Si el directorio no la
// registra, el mensaje se retira: un mensaje en Scheduled sin fila nunca saldria.
func (s *Service) storeScheduled(ctx context.Context, sess domain.Session, out domain.Outgoing, stored []byte, at time.Time, replaceUID uint32) (string, error) {
	var id string
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		folder, err := ensureScheduledFolder(ctx, mb, folders)
		if err != nil {
			return err
		}
		saved, err := mb.Append(ctx, folder, stored, scheduledFlags, out.Date)
		if err != nil {
			return err
		}
		if saved.UID == 0 || saved.UIDValidity == 0 {
			return unavailable(errors.New("el servidor IMAP no informo del UID del mensaje programado (UIDPLUS)"))
		}
		id, err = s.scheduled.CreateScheduled(ctx, domain.NewScheduledSend{
			Username: sess.Username, MessageID: out.MessageID, Folder: folder,
			UIDValidity: saved.UIDValidity, UID: saved.UID, SendAt: at,
			Subject: out.Subject, Recipients: out.Recipients(),
		})
		if err != nil {
			if _, xerr := mb.Expunge(context.WithoutCancel(ctx), folder, []uint32{saved.UID}); xerr != nil {
				s.logger.Error("webmail: no se pudo retirar el mensaje de un envio que no se registro", zap.String("username", sess.Username),
					zap.String("message_id", out.MessageID), zap.Error(xerr))
			}
			return s.scheduledError("no se pudo registrar el envio programado", sess, err)
		}
		if replaceUID != 0 {
			if _, err := s.retireDraft(ctx, mb, folders, replaceUID); err != nil {
				s.logger.Warn("webmail: no se pudo retirar el borrador programado", zap.String("username", sess.Username),
					zap.Uint32("uid", replaceUID), zap.Error(err))
			}
		}
		return nil
	})
	return id, err
}

// ensureScheduledFolder devuelve la carpeta de envios programados y la crea si el buzon aun no
// la tiene (Dovecot la declara con auto = no).
func ensureScheduledFolder(ctx context.Context, mb ports.Mailbox, folders []domain.Folder) (string, error) {
	if f, ok := domain.FolderWithRole(folders, domain.RoleScheduled); ok {
		return f.Name, nil
	}
	err := mb.CreateFolder(ctx, domain.ScheduledFolderName)
	if err != nil && !errors.Is(err, domain.ErrFolderExists) {
		return "", err
	}
	return domain.ScheduledFolderName, nil
}

func scheduleFingerprint(d domain.Draft, replaceUID uint32, at time.Time) string {
	return fingerprint(d, replaceUID) + ":" + at.UTC().Format(time.RFC3339)
}

// ListScheduled devuelve los envios programados del buzon de la sesion.
func (s *Service) ListScheduled(ctx context.Context, sess domain.Session) ([]domain.ScheduledSend, error) {
	rows, err := s.scheduled.ListScheduled(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudieron leer los envios programados", sess, err)
		return nil, unavailable(err)
	}
	return rows, nil
}

// Reschedule cambia la hora de un envio pendiente del buzon de la sesion.
func (s *Service) Reschedule(ctx context.Context, sess domain.Session, id string, at time.Time) (domain.ScheduledSend, error) {
	if err := domain.ValidateScheduledID(id); err != nil {
		return domain.ScheduledSend{}, err
	}
	if err := domain.ValidateSendAt(at, s.clock(), s.cfg.MaxScheduledDays); err != nil {
		return domain.ScheduledSend{}, err
	}
	row, err := s.scheduled.RescheduleScheduled(ctx, sess.Username, id, at)
	if err != nil {
		return domain.ScheduledSend{}, s.scheduledError("no se pudo reprogramar el envio", sess, err)
	}
	return row, nil
}

// CancelScheduled cancela un envio pendiente y devuelve su mensaje a Borradores. La fila se cancela
// antes de tocar el mensaje: desde ese momento el trabajador ya no puede reclamarla. Cancelar una
// fila ya cancelada no es un error (el directorio la da por cancelada y ya no la lista).
func (s *Service) CancelScheduled(ctx context.Context, sess domain.Session, id string) error {
	if err := domain.ValidateScheduledID(id); err != nil {
		return err
	}
	rows, err := s.scheduled.ListScheduled(ctx, sess.Username)
	if err != nil {
		return s.scheduledError("no se pudieron leer los envios programados", sess, err)
	}
	var row *domain.ScheduledSend
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
			break
		}
	}
	if err := s.scheduled.CancelScheduled(ctx, sess.Username, id); err != nil {
		return s.scheduledError("no se pudo cancelar el envio programado", sess, err)
	}
	if row == nil {
		return nil
	}
	err = s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		drafts, ok := domain.FolderWithRole(folders, domain.RoleDrafts)
		if !ok {
			return domain.ErrDraftsNotFound
		}
		uid, err := locateScheduled(ctx, mb, folders, row.Folder, row.UIDValidity, row.UID, row.MessageID)
		if err != nil || uid == 0 {
			return err
		}
		_, err = mb.Move(ctx, scheduledFolderOf(folders, row.Folder), []uint32{uid}, drafts.Name)
		return err
	})
	if err != nil {
		// La fila ya esta cancelada: el mensaje se queda en Scheduled, donde el usuario lo ve.
		s.logger.Warn("webmail: envio cancelado sin devolver el mensaje a Borradores", zap.String("username", sess.Username),
			zap.String("scheduled_id", id), zap.Error(err))
	}
	s.logger.Info("webmail: envio programado cancelado", zap.String("username", sess.Username), zap.String("scheduled_id", id))
	return nil
}

func (s *Service) scheduledError(msg string, sess domain.Session, err error) error {
	var verr *domain.ValidationError
	if errors.As(err, &verr) || errors.Is(err, domain.ErrScheduledNotFound) || errors.Is(err, domain.ErrScheduledNotPending) ||
		errors.Is(err, domain.ErrScheduledLimit) {
		return err
	}
	s.logFailure(msg, sess, err)
	return unavailable(err)
}

// scheduledFolderOf es la carpeta registrada en la fila si sigue existiendo, o la carpeta de
// envios programados actual.
func scheduledFolderOf(folders []domain.Folder, registered string) string {
	if _, ok := domain.FindFolder(folders, registered); ok && registered != "" {
		return registered
	}
	if f, ok := domain.FolderWithRole(folders, domain.RoleScheduled); ok {
		return f.Name
	}
	return registered
}

// locateScheduled encuentra el mensaje de una fila: por su UID si la carpeta conserva la
// UIDVALIDITY y el Message-ID coincide y, si no, por el Message-ID. 0 si ya no esta.
func locateScheduled(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, registered string, uidValidity, uid uint32, messageID string) (uint32, error) {
	folder := scheduledFolderOf(folders, registered)
	if _, ok := domain.FindFolder(folders, folder); !ok || folder == "" {
		return 0, nil
	}
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
	if !domain.IsValidMessageID(messageID) {
		return 0, nil
	}
	return mb.FindByMessageID(ctx, folder, messageID)
}

// RunScheduledSends es el trabajador de envios programados: cada intervalo reclama las filas
// vencidas de la celda y las envia. Si el lote vino lleno vuelve a reclamar sin esperar. Termina
// cuando se cancela ctx.
func (s *Service) RunScheduledSends(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.ScheduledPollInterval)
	defer ticker.Stop()
	for {
		for s.processScheduledBatch(ctx) == s.cfg.ScheduledBatch && ctx.Err() == nil {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scheduledItemTimeout acota un envio programado de principio a fin; el arriendo lo supera para
// que ninguna fila se reclame otra vez mientras sigue en curso.
func (s *Service) scheduledItemTimeout() time.Duration {
	return s.cfg.SendTimeout + postSendTimeout
}

func (s *Service) scheduledLease() time.Duration {
	return s.scheduledItemTimeout() + pendingMargin
}

// processScheduledBatch reclama un lote y lo envia en paralelo (el arriendo corre para todo el
// lote a la vez). Devuelve cuantas filas reclamo.
func (s *Service) processScheduledBatch(ctx context.Context) int {
	claims, err := s.scheduled.ClaimScheduled(ctx, s.cfg.ScheduledBatch, s.scheduledLease())
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("webmail: no se pudieron reclamar los envios programados", zap.Error(err))
		}
		return 0
	}
	var wg sync.WaitGroup
	for _, c := range claims {
		wg.Add(1)
		go func(c domain.ScheduledClaim) {
			defer wg.Done()
			s.runScheduled(ctx, c)
		}(c)
	}
	wg.Wait()
	return len(claims)
}

// runScheduled envia una fila reclamada y la cierra. Un desenlace sin estado deja la fila en su
// arriendo: otro intento la reclamara cuando venza.
func (s *Service) runScheduled(ctx context.Context, c domain.ScheduledClaim) {
	itemCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.scheduledItemTimeout())
	defer cancel()
	outcome := s.deliverScheduled(itemCtx, c)
	if outcome.Status == "" {
		return
	}
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
	defer cancelFinish()
	if err := s.scheduled.FinishScheduled(finishCtx, c.ID, outcome); err != nil {
		// La fila se vuelve a reclamar al vencer el arriendo; el registro de envios y la copia en
		// Enviados impiden que salga dos veces.
		s.logger.Error("webmail: no se pudo cerrar el envio programado", zap.String("scheduled_id", c.ID),
			zap.String("status", string(outcome.Status)), zap.Error(err))
		return
	}
	s.logger.Info("webmail: envio programado cerrado", zap.String("scheduled_id", c.ID), zap.String("username", c.Username),
		zap.String("status", string(outcome.Status)), zap.String("error", outcome.Error))
}

// maxOutcomeError acota el motivo que se guarda en la fila (puede traer la respuesta de Postfix).
const maxOutcomeError = 512

func failed(reason string, retry bool) domain.ScheduledOutcome {
	if len(reason) > maxOutcomeError {
		reason = strings.ToValidUTF8(reason[:maxOutcomeError], "")
	}
	return domain.ScheduledOutcome{Status: domain.ScheduledFailed, Error: reason, Retry: retry}
}

// deliverScheduled hace el envio de una fila reclamada y dice como cerrarla.
//
// Idempotencia: la fila reserva en el registro de envios una clave derivada de su id. Si Postfix
// acepto el mensaje y el cierre se perdio, el siguiente intento encuentra la clave enviada y solo
// cierra; si el registro se perdio (Redis descarta claves), la copia en Enviados con el mismo
// Message-ID dice lo mismo. Un envio cuya respuesta final se perdio no se repite nunca.
func (s *Service) deliverScheduled(ctx context.Context, c domain.ScheduledClaim) domain.ScheduledOutcome {
	username, ok := domain.NormalizeUsername(c.Username)
	if !ok || !domain.IsValidMessageID(c.MessageID) {
		return failed("fila invalida", false)
	}
	sess := domain.Session{Username: username}
	key := sendKey(username, "scheduled\x00"+c.ID)
	fp := "scheduled:" + c.ID
	rec, reserved, err := s.ledger.Reserve(ctx, key, domain.SendRecord{State: domain.SendPending, Fingerprint: fp}, s.cfg.SendTimeout+pendingMargin)
	if err != nil {
		s.logger.Warn("webmail: registro de envios no disponible para un envio programado", zap.String("scheduled_id", c.ID), zap.Error(err))
		return failed("registro de envios no disponible", true)
	}
	if !reserved {
		switch rec.State {
		case domain.SendSent:
			s.settleScheduled(ctx, sess, c, nil)
			return domain.ScheduledOutcome{Status: domain.ScheduledSent}
		case domain.SendUncertain:
			return failed("no se pudo confirmar si el servidor de correo acepto el mensaje", false)
		default:
			return domain.ScheduledOutcome{}
		}
	}

	stored, located, err := s.readScheduled(ctx, sess, c)
	if err != nil {
		s.release(ctx, key, rec.Token)
		s.logger.Warn("webmail: no se pudo leer el mensaje programado", zap.String("scheduled_id", c.ID), zap.Error(err))
		return failed("buzon no disponible", true)
	}
	if !located {
		sent, err := s.alreadyInSent(ctx, sess, c.MessageID)
		if err != nil {
			s.release(ctx, key, rec.Token)
			return failed("buzon no disponible", true)
		}
		if sent {
			rec.State, rec.MessageID, rec.SavedToSent = domain.SendSent, c.MessageID, true
			s.record(ctx, key, rec)
			return domain.ScheduledOutcome{Status: domain.ScheduledSent}
		}
		s.release(ctx, key, rec.Token)
		return domain.ScheduledOutcome{Status: domain.ScheduledCanceled, Error: "el mensaje ya no esta en la carpeta de envios programados"}
	}

	final, err := s.composer.Finalize(stored, s.clock())
	if err != nil {
		s.release(ctx, key, rec.Token)
		return failed("mensaje programado ilegible", false)
	}
	if outcome, ok := s.checkScheduledEnvelope(ctx, sess, &final); !ok {
		s.release(ctx, key, rec.Token)
		return outcome
	}
	if err := s.sender.Send(ctx, username, final.From, final.Recipients, final.Wire); err != nil {
		if errors.Is(err, domain.ErrDeliveryUncertain) {
			rec.State = domain.SendUncertain
			s.record(ctx, key, rec)
			s.logger.Error("webmail: no se pudo confirmar un envio programado; no se reintenta", zap.String("scheduled_id", c.ID), zap.Error(err))
			return failed(domain.ErrDeliveryUncertain.Error(), false)
		}
		s.release(ctx, key, rec.Token)
		s.logger.Warn("webmail: envio programado rechazado", zap.String("scheduled_id", c.ID), zap.String("username", username), zap.Error(err))
		return failed(err.Error(), errors.Is(err, domain.ErrUnavailable))
	}
	s.logger.Info("webmail: envio programado entregado", zap.String("scheduled_id", c.ID), zap.String("username", username),
		zap.String("message_id", c.MessageID), zap.Int("recipients", len(final.Recipients)))
	rec.State, rec.MessageID = domain.SendSent, c.MessageID
	s.record(ctx, key, rec)
	rec.SavedToSent = s.settleScheduled(ctx, sess, c, final.Stored)
	s.record(ctx, key, rec)
	return domain.ScheduledOutcome{Status: domain.ScheduledSent}
}

// checkScheduledEnvelope vuelve a aplicar lo que se comprobo al programar y pudo cambiar desde
// entonces: el remitente debe seguir siendo del buzon y los destinatarios caber en el tope.
func (s *Service) checkScheduledEnvelope(ctx context.Context, sess domain.Session, final *domain.FinalizedMessage) (domain.ScheduledOutcome, bool) {
	if len(final.Recipients) == 0 {
		return failed("el mensaje programado no tiene destinatarios", false), false
	}
	if len(final.Recipients) > s.cfg.Limits.MaxRecipients {
		return failed(domain.ErrTooManyRecipients.Error(), false), false
	}
	from := domain.Address{Email: final.From}
	if err := s.checkSender(ctx, sess, &from); err != nil {
		if errors.Is(err, domain.ErrSenderNotAllowed) {
			return failed(domain.ErrSenderNotAllowed.Error(), false), false
		}
		return failed("directorio no disponible", true), false
	}
	final.From = from.Email
	return domain.ScheduledOutcome{}, true
}

// readScheduled lee el mensaje de la fila. located es false si ya no esta en Scheduled.
func (s *Service) readScheduled(ctx context.Context, sess domain.Session, c domain.ScheduledClaim) ([]byte, bool, error) {
	var raw []byte
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		uid, err := locateScheduled(ctx, mb, folders, c.Folder, c.UIDValidity, c.UID, c.MessageID)
		if err != nil || uid == 0 {
			return err
		}
		_, body, err := mb.OpenRaw(ctx, scheduledFolderOf(folders, c.Folder), uid, s.cfg.Limits.MaxMessageBytes)
		if err != nil {
			return err
		}
		defer body.Close()
		raw, err = io.ReadAll(body)
		return err
	})
	if errors.Is(err, domain.ErrMessageNotFound) || errors.Is(err, domain.ErrFolderNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, raw != nil, nil
}

func (s *Service) alreadyInSent(ctx context.Context, sess domain.Session, messageID string) (bool, error) {
	found := false
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		sent, ok := domain.FolderWithRole(folders, domain.RoleSent)
		if !ok {
			return nil
		}
		uid, err := mb.FindByMessageID(ctx, sent.Name, messageID)
		found = uid != 0
		return err
	})
	return found, err
}

// settleScheduled deja la copia en Enviados (si no estaba ya) y retira el mensaje de Scheduled.
// stored es la copia a guardar; nil la vuelve a preparar desde Scheduled. Como tras un envio
// normal, el mensaje solo sale de Scheduled si su copia quedo en Enviados. Devuelve si la copia
// esta en Enviados.
func (s *Service) settleScheduled(ctx context.Context, sess domain.Session, c domain.ScheduledClaim, stored []byte) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postSendTimeout)
	defer cancel()
	saved := false
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		sent, ok := domain.FolderWithRole(folders, domain.RoleSent)
		if !ok {
			return errors.New("el buzon no tiene carpeta de enviados")
		}
		uid, err := locateScheduled(ctx, mb, folders, c.Folder, c.UIDValidity, c.UID, c.MessageID)
		if err != nil {
			return err
		}
		inSent, err := mb.FindByMessageID(ctx, sent.Name, c.MessageID)
		if err != nil {
			return err
		}
		if inSent == 0 {
			if stored == nil && uid != 0 {
				stored, err = s.finalizedCopy(ctx, mb, scheduledFolderOf(folders, c.Folder), uid)
				if err != nil {
					return err
				}
			}
			if stored == nil {
				return errors.New("no queda copia del mensaje para Enviados")
			}
			if _, err := mb.Append(ctx, sent.Name, stored, []domain.Flag{domain.FlagSeen}, s.clock()); err != nil {
				return err
			}
		}
		saved = true
		if uid != 0 {
			_, err = mb.Expunge(ctx, scheduledFolderOf(folders, c.Folder), []uint32{uid})
		}
		return err
	})
	if err != nil {
		s.logger.Warn("webmail: envio programado sin completar Enviados o Scheduled", zap.String("scheduled_id", c.ID),
			zap.String("username", sess.Username), zap.Error(err))
	}
	return saved
}

func (s *Service) finalizedCopy(ctx context.Context, mb ports.Mailbox, folder string, uid uint32) ([]byte, error) {
	_, body, err := mb.OpenRaw(ctx, folder, uid, s.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	final, err := s.composer.Finalize(raw, s.clock())
	if err != nil {
		return nil, err
	}
	return final.Stored, nil
}
