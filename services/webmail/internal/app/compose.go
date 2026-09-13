package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	// postSendTimeout es lo que se dedica a guardar la copia en Enviados. Corre sin la
	// cancelacion de la peticion: el mensaje ya salio y su copia no debe perderse porque
	// el navegador cierre la conexion.
	postSendTimeout = 30 * time.Second
)

// Send entrega el borrador por el submission de la celda y guarda la copia en Enviados.
//
// El remitente es el propio buzon salvo que el cliente pida otro; en ese caso lo decide
// Postfix con smtpd_sender_login_maps (el propio buzon o lo que permita sender_acl), no
// el webmail. El From de la cabecera es siempre el mismo que el del sobre.
func (s *Service) Send(ctx context.Context, sess domain.Session, d domain.Draft) (domain.SendResult, error) {
	if err := s.prepare(ctx, sess, &d, true); err != nil {
		return domain.SendResult{}, err
	}
	out := s.outgoing(d)
	if d.InReplyTo != nil {
		err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
			return s.chainReply(ctx, mb, &out)
		})
		if err != nil {
			return domain.SendResult{}, err
		}
	}
	wire, err := s.compose(out, false)
	if err != nil {
		return domain.SendResult{}, err
	}
	stored, err := s.compose(out, true)
	if err != nil {
		return domain.SendResult{}, err
	}

	recipients := d.Recipients()
	if err := s.sender.Send(ctx, sess.Username, d.From.Email, recipients, wire); err != nil {
		s.logger.Warn("webmail: envio rechazado", zap.String("username", sess.Username),
			zap.String("from", d.From.Email), zap.Int("recipients", len(recipients)), zap.Error(err))
		return domain.SendResult{}, err
	}
	s.logger.Info("webmail: mensaje enviado", zap.String("username", sess.Username),
		zap.String("message_id", out.MessageID), zap.Int("recipients", len(recipients)))

	return domain.SendResult{MessageID: out.MessageID, SavedToSent: s.afterSend(ctx, sess, stored, out)}, nil
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
		uid, err = mb.Append(ctx, drafts.Name, stored, []domain.Flag{domain.FlagDraft, domain.FlagSeen}, out.Date)
		if err != nil {
			return err
		}
		if replaceUID != 0 && replaceUID != uid {
			if err := mb.Expunge(ctx, drafts.Name, replaceUID); err != nil && !errors.Is(err, domain.ErrMessageNotFound) {
				s.logger.Warn("webmail: no se pudo retirar el borrador anterior", zap.String("username", sess.Username),
					zap.Uint32("uid", replaceUID), zap.Error(err))
			}
		}
		return nil
	})
	return uid, err
}

// prepare completa el remitente, sanea el HTML propio, valida y analiza los adjuntos.
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
	validate := d.ValidateForSave
	if forSend {
		validate = d.ValidateForSend
	}
	if err := validate(s.cfg.Limits); err != nil {
		return err
	}
	if d.InReplyTo != nil {
		if err := domain.ValidateFolderName(d.InReplyTo.Folder); err != nil {
			return err
		}
		if d.InReplyTo.UID == 0 {
			return domain.NewValidationError("in_reply_to", "debe ser un UID valido")
		}
	}
	return s.scan(ctx, sess, d.Attachments)
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

// afterSend guarda la copia en Enviados y marca como respondido el original. Nada de
// esto deshace el envio: un fallo se registra y el cliente lo ve en saved_to_sent.
func (s *Service) afterSend(ctx context.Context, sess domain.Session, stored []byte, out domain.Outgoing) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postSendTimeout)
	defer cancel()
	saved := false
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
			if err := mb.SetFlags(ctx, target.Folder, target.UID, change); err != nil {
				s.logger.Warn("webmail: no se pudo marcar el original como respondido", zap.String("username", sess.Username), zap.Error(err))
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Warn("webmail: no se pudo abrir el buzon tras el envio", zap.String("username", sess.Username), zap.Error(err))
	}
	return saved
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
