package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// withMailbox abre el buzon de la sesion, ejecuta fn y cierra la conexion.
func (s *Service) withMailbox(ctx context.Context, sess domain.Session, fn func(ports.Mailbox) error) error {
	mb, err := s.mail.Open(ctx, sess.Username)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := mb.Close(); cerr != nil {
			s.logger.Debug("webmail: cierre de la conexion IMAP", zap.Error(cerr))
		}
	}()
	return fn(mb)
}

// Quota devuelve la cuota del buzon o nil si no se pudo leer: la cuota es informativa y
// su ausencia no debe impedir abrir la sesion.
func (s *Service) Quota(ctx context.Context, sess domain.Session) *domain.Quota {
	var q *domain.Quota
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		q, err = mb.Quota(ctx)
		return err
	})
	if err != nil {
		s.logger.Warn("webmail: no se pudo leer la cuota", zap.String("username", sess.Username), zap.Error(err))
		return nil
	}
	return q
}

// Folders lista las carpetas con totales y no leidos.
func (s *Service) Folders(ctx context.Context, sess domain.Session) ([]domain.Folder, error) {
	var out []domain.Folder
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		out, err = mb.Folders(ctx, true)
		return err
	})
	return out, err
}

// ListMessages devuelve una pagina de sobres de la carpeta, del mas reciente al mas antiguo.
func (s *Service) ListMessages(ctx context.Context, sess domain.Session, folder string, q domain.ListQuery) (domain.MessagePage, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return domain.MessagePage{}, err
	}
	var page domain.MessagePage
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		page, err = mb.List(ctx, folder, q)
		return err
	})
	return page, err
}

// ReadMessage devuelve el mensaje con el HTML saneado. peek evita marcarlo como leido;
// allowRemoteImages levanta el bloqueo de imagenes remotas solo para esta lectura.
func (s *Service) ReadMessage(ctx context.Context, sess domain.Session, folder string, uid uint32, peek, allowRemoteImages bool) (*domain.Message, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return nil, err
	}
	var raw *domain.RawMessage
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		raw, err = mb.Read(ctx, folder, uid, domain.ReadOptions{MarkSeen: !peek, MaxBodyBytes: s.cfg.MaxBodyPartBytes})
		return err
	})
	if err != nil {
		return nil, err
	}

	cids := make(map[string]string, len(raw.Parts))
	for _, p := range raw.Parts {
		if p.ContentID != "" {
			cids[p.ContentID] = s.partURL(folder, uid, p.ID)
		}
	}
	clean := s.sanitizer.Incoming(raw.HTML, domain.SanitizeOptions{
		AllowRemoteImages: allowRemoteImages,
		ResolveCID: func(cid string) (string, bool) {
			u, ok := cids[strings.Trim(cid, "<>")]
			return u, ok
		},
	})
	return &domain.Message{
		Envelope:      raw.Envelope,
		Folder:        folder,
		Bcc:           raw.Bcc,
		ReplyTo:       raw.ReplyTo,
		MessageID:     raw.MessageID,
		InReplyTo:     raw.InReplyTo,
		References:    raw.References,
		Text:          raw.Text,
		TextTruncated: raw.TextTruncated,
		HTML:          clean.HTML,
		HTMLTruncated: raw.HTMLTruncated,
		RemoteImages:  domain.RemoteImages{Present: clean.RemoteImages, Blocked: clean.RemoteImages && !allowRemoteImages},
		Attachments:   raw.Parts,
	}, nil
}

// StreamPart entrega una parte decodificada a emit, que escribe la respuesta. La conexion
// IMAP vive lo que dura la entrega.
func (s *Service) StreamPart(ctx context.Context, sess domain.Session, folder string, uid uint32, partID string, emit func(domain.Part, io.Reader) error) error {
	if err := domain.ValidateFolderName(folder); err != nil {
		return err
	}
	if _, err := domain.ParsePartID(partID); err != nil {
		return err
	}
	return s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		part, body, err := mb.OpenPart(ctx, folder, uid, partID, s.cfg.MaxAttachmentBytes)
		if err != nil {
			return err
		}
		defer body.Close()
		return emit(part, body)
	})
}

// ChangeFlags anade y quita \Seen, \Flagged y \Answered.
func (s *Service) ChangeFlags(ctx context.Context, sess domain.Session, folder string, uid uint32, change domain.FlagChange) error {
	if err := domain.ValidateFolderName(folder); err != nil {
		return err
	}
	return s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		return requireOne(mb.SetFlags(ctx, folder, []uint32{uid}, change))
	})
}

// Move mueve un mensaje a otra carpeta del mismo buzon.
func (s *Service) Move(ctx context.Context, sess domain.Session, folder string, uid uint32, dest string) error {
	if err := domain.ValidateFolderName(folder); err != nil {
		return err
	}
	if err := domain.ValidateFolderName(dest); err != nil {
		var verr *domain.ValidationError
		if errors.As(err, &verr) {
			return domain.NewValidationError("to", verr.Reason)
		}
		return err
	}
	if dest == folder {
		return domain.NewValidationError("to", "el mensaje ya esta en esa carpeta")
	}
	return s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		return requireOne(mb.Move(ctx, folder, []uint32{uid}, dest))
	})
}

// Delete mueve el mensaje a la papelera; desde la papelera lo borra de forma definitiva.
// permanent indica cual de las dos cosas ocurrio.
func (s *Service) Delete(ctx context.Context, sess domain.Session, folder string, uid uint32) (bool, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return false, err
	}
	var res domain.BatchResult
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		res, err = s.deleteMessages(ctx, mb, folder, []uint32{uid})
		if err == nil && res.Affected == 0 {
			return domain.ErrMessageNotFound
		}
		return err
	})
	if err != nil {
		return false, err
	}
	return res.Permanent, nil
}

// Batch aplica una accion a varios mensajes de la carpeta en una sola conexion. Affected cuenta
// los que existian: un UID que ya no esta (otro cliente lo movio) no es un error.
func (s *Service) Batch(ctx context.Context, sess domain.Session, folder string, b domain.Batch) (domain.BatchResult, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return domain.BatchResult{}, err
	}
	var res domain.BatchResult
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		switch b.Action {
		case domain.BatchFlags:
			res.Affected, err = mb.SetFlags(ctx, folder, b.UIDs, b.Flags)
		case domain.BatchMove:
			res.Affected, err = mb.Move(ctx, folder, b.UIDs, b.To)
		case domain.BatchDelete:
			res, err = s.deleteMessages(ctx, mb, folder, b.UIDs)
		default:
			err = domain.NewValidationError("action", "accion desconocida")
		}
		return err
	})
	return res, err
}

// deleteMessages mueve a la papelera o, desde la papelera, borra para siempre.
func (s *Service) deleteMessages(ctx context.Context, mb ports.Mailbox, folder string, uids []uint32) (domain.BatchResult, error) {
	folders, err := mb.Folders(ctx, false)
	if err != nil {
		return domain.BatchResult{}, err
	}
	trash, ok := domain.FolderWithRole(folders, domain.RoleTrash)
	if !ok {
		return domain.BatchResult{}, domain.ErrTrashNotFound
	}
	if trash.Name == folder {
		n, err := mb.Expunge(ctx, folder, uids)
		return domain.BatchResult{Affected: n, Permanent: true}, err
	}
	n, err := mb.Move(ctx, folder, uids, trash.Name)
	return domain.BatchResult{Affected: n}, err
}

// StreamRaw entrega el mensaje original (.eml) a emit, acotado por el tope de descarga.
func (s *Service) StreamRaw(ctx context.Context, sess domain.Session, folder string, uid uint32, emit func(domain.StoredMessage, io.Reader) error) error {
	if err := domain.ValidateFolderName(folder); err != nil {
		return err
	}
	return s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		msg, body, err := mb.OpenRaw(ctx, folder, uid, s.cfg.MaxAttachmentBytes)
		if err != nil {
			return err
		}
		defer body.Close()
		return emit(msg, body)
	})
}

// requireOne traduce una operacion sobre un solo UID que no encontro el mensaje.
func requireOne(n int, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrMessageNotFound
	}
	return nil
}
