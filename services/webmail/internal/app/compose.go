package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

const (
	// maxReferences acota la cadena References de una respuesta: los hilos largos la
	// hacen crecer sin limite y solo importan los ultimos eslabones.
	maxReferences = 20
	// postSendTimeout es lo que se dedica a guardar la copia en Enviados y retirar el
	// borrador. Corre sin la cancelacion de la peticion: el mensaje ya salio y su copia no
	// debe perderse porque el navegador cierre la conexion.
	postSendTimeout = 30 * time.Second
)

// Send entrega el borrador por el submission de la celda, guarda la copia en Enviados y
// retira el borrador que reemplaza (opts.ReplaceUID).
//
// Idempotencia: la clave del cliente se reserva en el registro de envios antes de tocar
// nada. Mientras el envio esta en curso, otra peticion con la misma clave recibe
// ErrSendInProgress. En cuanto Postfix acepta el mensaje la clave queda como enviada, antes
// de guardar la copia o retirar el borrador: un reintento con la misma clave devuelve el
// resultado guardado sin entregar nada y, si el borrador no se pudo retirar, lo vuelve a
// intentar. Si el mensaje no salio (validacion, adjunto, rechazo de Postfix) la clave se
// libera y se puede reintentar con ella. Si la respuesta de Postfix al final de DATA se
// pierde, la clave queda en estado incierto y no se reintenta sola: el mensaje pudo quedar
// en cola, y reenviarlo lo decide el usuario con otra clave.
//
// El remitente es el propio buzon salvo que el cliente pida otro de sus remitentes (los que
// el directorio de la celda le permite con la regla de Postfix). Postfix vuelve a aplicar
// smtpd_sender_login_maps al buzon real: el webmail no puede saltarsela. El From de la
// cabecera es siempre el mismo que el del sobre.
func (s *Service) Send(ctx context.Context, sess domain.Session, d domain.Draft, opts domain.SendOptions) (domain.SendResult, error) {
	if err := domain.ValidateIdempotencyKey(opts.IdempotencyKey); err != nil {
		return domain.SendResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer cancel()

	key := sendKey(sess.Username, opts.IdempotencyKey)
	fp := fingerprint(d, opts.ReplaceUID)
	rec, reserved, err := s.ledger.Reserve(ctx, key,
		domain.SendRecord{State: domain.SendPending, Fingerprint: fp}, s.cfg.SendTimeout+pendingMargin)
	if err != nil {
		s.logger.Error("webmail: no se pudo reservar la clave del envio", zap.String("username", sess.Username), zap.Error(err))
		return domain.SendResult{}, unavailable(err)
	}
	if !reserved {
		return s.replay(ctx, sess, key, rec, fp, opts.ReplaceUID)
	}
	return s.deliver(ctx, sess, d, opts.ReplaceUID, key, rec)
}

// deliver hace el envio de una clave recien reservada.
func (s *Service) deliver(ctx context.Context, sess domain.Session, d domain.Draft, replaceUID uint32, key string, rec domain.SendRecord) (domain.SendResult, error) {
	out, wire, stored, err := s.build(ctx, sess, d)
	if err != nil {
		s.release(ctx, key, rec.Token)
		return domain.SendResult{}, err
	}
	recipients := out.Recipients()
	if err := s.sender.Send(ctx, sess.Username, out.From.Email, recipients, wire); err != nil {
		if errors.Is(err, domain.ErrDeliveryUncertain) {
			rec.State = domain.SendUncertain
			s.record(ctx, key, rec)
			s.logger.Error("webmail: no se pudo confirmar el envio; la clave no se reintenta", zap.String("username", sess.Username),
				zap.String("message_id", out.MessageID), zap.Error(err))
			return domain.SendResult{}, err
		}
		s.release(ctx, key, rec.Token)
		s.logger.Warn("webmail: envio rechazado", zap.String("username", sess.Username),
			zap.String("from", out.From.Email), zap.Int("recipients", len(recipients)), zap.Error(err))
		return domain.SendResult{}, err
	}
	s.logger.Info("webmail: mensaje enviado", zap.String("username", sess.Username),
		zap.String("message_id", out.MessageID), zap.Int("recipients", len(recipients)))

	rec.State, rec.MessageID = domain.SendSent, out.MessageID
	s.record(ctx, key, rec)
	rec.SavedToSent, rec.DraftRemoved = s.afterSend(ctx, sess, stored, out, replaceUID)
	s.record(ctx, key, rec)
	return domain.SendResult{MessageID: out.MessageID, SavedToSent: rec.SavedToSent, DraftRemoved: rec.DraftRemoved}, nil
}

// replay responde a una clave que ya tenia registro sin entregar nada.
func (s *Service) replay(ctx context.Context, sess domain.Session, key string, rec domain.SendRecord, fp string, replaceUID uint32) (domain.SendResult, error) {
	if rec.Fingerprint != fp {
		s.logger.Warn("webmail: clave de envio repetida con otro mensaje", zap.String("username", sess.Username))
		return domain.SendResult{}, domain.ErrIdempotencyKeyReused
	}
	switch rec.State {
	case domain.SendPending:
		return domain.SendResult{}, domain.ErrSendInProgress
	case domain.SendUncertain:
		return domain.SendResult{}, domain.ErrDeliveryUncertain
	case domain.SendSent:
	default:
		return domain.SendResult{}, unavailable(fmt.Errorf("estado de envío desconocido %q", rec.State))
	}
	if replaceUID != 0 && rec.SavedToSent && !rec.DraftRemoved {
		err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
			folders, err := mb.Folders(ctx, false)
			if err != nil {
				return err
			}
			rec.DraftRemoved, err = s.retireDraft(ctx, mb, folders, replaceUID)
			return err
		})
		if err != nil {
			s.logger.Warn("webmail: el reintento no pudo retirar el borrador", zap.String("username", sess.Username),
				zap.Uint32("uid", replaceUID), zap.Error(err))
		}
		if rec.DraftRemoved {
			s.record(ctx, key, rec)
		}
	}
	s.logger.Info("webmail: envio repetido con la misma clave; no se entrega de nuevo",
		zap.String("username", sess.Username), zap.String("message_id", rec.MessageID))
	return domain.SendResult{MessageID: rec.MessageID, SavedToSent: rec.SavedToSent, DraftRemoved: rec.DraftRemoved, Replayed: true}, nil
}

// build prepara el borrador y compone la version que viaja (sin Bcc) y la que se guarda.
func (s *Service) build(ctx context.Context, sess domain.Session, d domain.Draft) (domain.Outgoing, []byte, []byte, error) {
	if err := s.prepare(ctx, sess, &d, true); err != nil {
		return domain.Outgoing{}, nil, nil, err
	}
	out := s.outgoing(d)
	if d.InReplyTo != nil {
		err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
			return s.chainReply(ctx, mb, &out)
		})
		if err != nil {
			return domain.Outgoing{}, nil, nil, err
		}
	}
	wire, err := s.compose(out, false)
	if err != nil {
		return domain.Outgoing{}, nil, nil, err
	}
	stored, err := s.compose(out, true)
	if err != nil {
		return domain.Outgoing{}, nil, nil, err
	}
	return out, wire, stored, nil
}

// SaveDraft guarda el borrador en la carpeta de borradores. replaceUID es el borrador
// anterior del mismo mensaje, que se borra una vez guardado el nuevo.
func (s *Service) SaveDraft(ctx context.Context, sess domain.Session, d domain.Draft, replaceUID uint32) (uint32, error) {
	if err := s.prepare(ctx, sess, &d, false); err != nil {
		return 0, err
	}
	out := s.outgoing(d)
	var uid uint32
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		if d.InReplyTo != nil {
			if err := s.chainReply(ctx, mb, &out); err != nil {
				return err
			}
		}
		stored, err := s.compose(out, true)
		if err != nil {
			return err
		}
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		drafts, ok := domain.FolderWithRole(folders, domain.RoleDrafts)
		if !ok {
			return domain.ErrDraftsNotFound
		}
		saved, err := mb.Append(ctx, drafts.Name, stored, []domain.Flag{domain.FlagDraft, domain.FlagSeen}, out.Date)
		if err != nil {
			return err
		}
		uid = saved.UID
		if replaceUID != 0 && replaceUID != uid {
			if _, err := mb.Expunge(ctx, drafts.Name, []uint32{replaceUID}); err != nil {
				s.logger.Warn("webmail: no se pudo retirar el borrador anterior", zap.String("username", sess.Username),
					zap.Uint32("uid", replaceUID), zap.Error(err))
			}
		}
		return nil
	})
	return uid, err
}

// prepare completa el remitente, sanea el HTML propio, valida, comprueba el remitente,
// trae los adjuntos del buzon y analiza todos los adjuntos. Lo barato va primero: una
// peticion invalida no llega al directorio, a Dovecot ni a ClamAV.
func (s *Service) prepare(ctx context.Context, sess domain.Session, d *domain.Draft, forSend bool) error {
	switch {
	case d.From.Email == "":
		d.From = domain.Address{Name: sess.DisplayName, Email: sess.Username}
	case strings.EqualFold(d.From.Email, sess.Username) && d.From.Name == "":
		d.From.Name = sess.DisplayName
	}
	if d.HTML != "" {
		clean, plain := s.sanitizer.Outgoing(d.HTML)
		d.HTML = clean
		if strings.TrimSpace(d.Text) == "" {
			d.Text = plain
		}
	}
	for i := range d.Attachments {
		d.Attachments[i].Filename = domain.SanitizeFilename(d.Attachments[i].Filename)
		d.Attachments[i].ContentType = domain.NormalizeAttachmentType(d.Attachments[i].ContentType)
	}
	validate := func() error {
		if forSend {
			return d.ValidateForSend(s.cfg.Limits)
		}
		return d.ValidateForSave(s.cfg.Limits)
	}
	if err := validate(); err != nil {
		return err
	}
	if d.InReplyTo != nil {
		if err := domain.ValidateFolderName(d.InReplyTo.Folder); err != nil {
			return err
		}
		if d.InReplyTo.UID == 0 {
			return domain.NewValidationError("in_reply_to", "debe ser un UID válido")
		}
	}
	if err := s.checkSender(ctx, sess, &d.From); err != nil {
		return err
	}
	if d.Source != nil {
		if err := s.resolveSource(ctx, sess, d); err != nil {
			return err
		}
		if err := validate(); err != nil {
			return err
		}
	}
	return s.scan(ctx, sess, d.Attachments)
}

// checkSender admite el propio buzon y las direcciones concretas que el directorio de la
// celda le permite con la regla de Postfix; cualquier otra se rechaza antes de llegar a
// Postfix. El propio buzon no consulta el directorio: enviar como uno mismo no depende de
// que mail-directory responda.
func (s *Service) checkSender(ctx context.Context, sess domain.Session, from *domain.Address) error {
	if strings.EqualFold(from.Email, sess.Username) {
		from.Email = sess.Username
		return nil
	}
	identities, err := s.directory.SenderIdentities(ctx, sess.Username)
	if err != nil {
		s.logger.Error("webmail: no se pudieron leer los remitentes del buzon", zap.String("username", sess.Username), zap.Error(err))
		return unavailable(err)
	}
	for _, id := range identities {
		if strings.EqualFold(id, from.Email) {
			from.Email = strings.ToLower(id)
			return nil
		}
	}
	s.logger.Warn("webmail: remitente que el buzon no puede usar", zap.String("username", sess.Username), zap.String("from", from.Email))
	return domain.ErrSenderNotAllowed
}

// resolveSource trae del buzon las partes pedidas y las anade como adjuntos. Cada parte se
// lee acotada por el tope de descarga y por lo que le queda al mensaje; su nombre y su tipo
// salen de la estructura del mensaje guardado, no de lo que diga el cliente.
func (s *Service) resolveSource(ctx context.Context, sess domain.Session, d *domain.Draft) error {
	src := d.Source
	budget := s.cfg.Limits.MaxMessageBytes - d.ContentBytes()
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		for _, id := range src.Parts {
			limit := min(s.cfg.MaxAttachmentBytes, budget)
			if limit < 1 {
				return domain.ErrMessageTooLarge
			}
			part, data, err := readSourcePart(ctx, mb, src, id, limit)
			if errors.Is(err, domain.ErrPartTooLarge) && budget <= s.cfg.MaxAttachmentBytes {
				return domain.ErrMessageTooLarge
			}
			if err != nil {
				return err
			}
			budget -= int64(len(data))
			d.Attachments = append(d.Attachments, domain.Attachment{
				Filename:    domain.SanitizeFilename(part.Filename),
				ContentType: domain.NormalizeAttachmentType(part.ContentType),
				Data:        data,
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.Source = nil
	return nil
}

func readSourcePart(ctx context.Context, mb ports.Mailbox, src *domain.PartSource, id string, limit int64) (domain.Part, []byte, error) {
	part, body, err := mb.OpenPart(ctx, src.Folder, src.UID, id, limit)
	if err != nil {
		return domain.Part{}, nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		if errors.Is(err, domain.ErrPartTooLarge) {
			return domain.Part{}, nil, err
		}
		return domain.Part{}, nil, unavailable(err)
	}
	return part, data, nil
}

// scan analiza cada adjunto antes de enviarlo o guardarlo en el buzon. Falla cerrado:
// si ClamAV no responde, el adjunto no entra.
func (s *Service) scan(ctx context.Context, sess domain.Session, attachments []domain.Attachment) error {
	if s.scanner == nil {
		return nil
	}
	for _, a := range attachments {
		if err := s.scanner.Scan(ctx, a.Filename, a.Data); err != nil {
			if errors.Is(err, domain.ErrAttachmentInfected) {
				s.logger.Warn("webmail: adjunto con malware rechazado", zap.String("username", sess.Username),
					zap.String("filename", a.Filename), zap.Error(err))
				return domain.ErrAttachmentInfected
			}
			s.logger.Error("webmail: no se pudo analizar un adjunto", zap.String("username", sess.Username), zap.Error(err))
			return domain.ErrScanUnavailable
		}
	}
	return nil
}

func (s *Service) outgoing(d domain.Draft) domain.Outgoing {
	return domain.Outgoing{Draft: d, MessageID: newMessageID(d.From.Email), Date: s.clock()}
}

// chainReply encadena la respuesta con el original: In-Reply-To y References salen del
// mensaje guardado en el buzon, no de lo que diga el cliente.
func (s *Service) chainReply(ctx context.Context, mb ports.Mailbox, out *domain.Outgoing) error {
	target := out.Draft.InReplyTo
	ref, err := mb.ReplyReference(ctx, target.Folder, target.UID)
	if err != nil {
		return err
	}
	var refs []string
	for _, id := range ref.References {
		if domain.IsValidMessageID(id) {
			refs = append(refs, id)
		}
	}
	if domain.IsValidMessageID(ref.MessageID) {
		out.InReplyTo = ref.MessageID
		refs = append(refs, ref.MessageID)
	}
	if len(refs) > maxReferences {
		refs = refs[len(refs)-maxReferences:]
	}
	out.References = refs
	return nil
}

// compose arma el mensaje y aplica el tope sobre lo que de verdad viaja.
func (s *Service) compose(out domain.Outgoing, includeBcc bool) ([]byte, error) {
	raw, err := s.composer.Compose(out, includeBcc)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.cfg.Limits.MaxMessageBytes {
		return nil, domain.ErrMessageTooLarge
	}
	return raw, nil
}

// afterSend guarda la copia en Enviados, marca como respondido el original y retira el
// borrador que el envio reemplaza. Nada de esto deshace el envio: un fallo se registra y el
// cliente lo ve en saved_to_sent y draft_removed. El borrador solo se retira si la copia
// quedo en Enviados: si no, es el unico registro de lo que salio.
func (s *Service) afterSend(ctx context.Context, sess domain.Session, stored []byte, out domain.Outgoing, replaceUID uint32) (saved, removed bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postSendTimeout)
	defer cancel()
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		if sent, ok := domain.FolderWithRole(folders, domain.RoleSent); ok {
			if _, err := mb.Append(ctx, sent.Name, stored, []domain.Flag{domain.FlagSeen}, out.Date); err != nil {
				s.logger.Warn("webmail: no se pudo guardar la copia en Enviados", zap.String("username", sess.Username), zap.Error(err))
			} else {
				saved = true
			}
		} else {
			s.logger.Warn("webmail: el buzon no tiene carpeta de enviados", zap.String("username", sess.Username))
		}
		if target := out.Draft.InReplyTo; target != nil {
			change := domain.FlagChange{Add: []domain.Flag{domain.FlagAnswered}}
			if _, err := mb.SetFlags(ctx, target.Folder, []uint32{target.UID}, change); err != nil {
				s.logger.Warn("webmail: no se pudo marcar el original como respondido", zap.String("username", sess.Username), zap.Error(err))
			}
		}
		if replaceUID != 0 && saved {
			if removed, err = s.retireDraft(ctx, mb, folders, replaceUID); err != nil {
				s.logger.Warn("webmail: no se pudo retirar el borrador enviado", zap.String("username", sess.Username),
					zap.Uint32("uid", replaceUID), zap.Error(err))
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Warn("webmail: no se pudo abrir el buzon tras el envio", zap.String("username", sess.Username), zap.Error(err))
	}
	return saved, removed
}

// retireDraft borra de Borradores el borrador que el envio reemplaza. Si ya no estaba,
// cuenta como retirado.
func (s *Service) retireDraft(ctx context.Context, mb ports.Mailbox, folders []domain.Folder, uid uint32) (bool, error) {
	drafts, ok := domain.FolderWithRole(folders, domain.RoleDrafts)
	if !ok {
		return false, domain.ErrDraftsNotFound
	}
	if _, err := mb.Expunge(ctx, drafts.Name, []uint32{uid}); err != nil {
		return false, err
	}
	return true, nil
}

// newMessageID genera un Message-ID con el dominio del remitente (sin corchetes).
func newMessageID(from string) string {
	host := "localhost"
	if at := strings.LastIndexByte(from, '@'); at >= 0 && at < len(from)-1 {
		host = from[at+1:]
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Sin aleatoriedad se usa la hora: el identificador deja de ser impredecible
		// pero sigue siendo unico en la practica, y no hay secreto que dependa de el.
		return time.Now().UTC().Format("20060102150405.000000000") + "@" + host
	}
	return hex.EncodeToString(b) + "@" + host
}
