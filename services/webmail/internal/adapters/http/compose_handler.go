package http

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	// multipartOverhead son las cabeceras y fronteras del formulario, que no cuentan
	// como contenido del mensaje.
	multipartOverhead = 1 << 20
	attachmentField   = "attachments"
	maxListValues     = 200
)

// idempotencyHeader identifica el intento de envio del cliente: un reintento con la misma
// clave no vuelve a entregar el mensaje.
const idempotencyHeader = "Idempotency-Key"

// composeFields son los campos de texto admitidos; cualquier otro se rechaza.
var composeFields = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "subject": true, "text": true, "html": true,
	"in_reply_to": true, "in_reply_to_folder": true, "replace_uid": true,
	"source_folder": true, "source_uid": true, "source_parts": true, "send_at": true,
	"follow_up_days": true,
}

// listFields admiten varios valores (y las direcciones, cada valor una lista separada por
// comas).
var listFields = map[string]bool{"to": true, "cc": true, "bcc": true, "source_parts": true}

type composeForm struct {
	draft      domain.Draft
	replaceUID uint32
	// sendAt, si no es cero, programa el envio en vez de entregarlo ya.
	sendAt time.Time
	// followUpDays, si no es cero, pide seguimiento: avisar si nadie responde en esos dias.
	followUpDays int
}

// Send entrega el mensaje. replace_uid retira ese borrador en la misma operacion y
// source_folder, source_uid y source_parts adjuntan partes de un mensaje del buzon. Con send_at el
// mensaje no sale: queda programado y la respuesta es {"scheduled":{"id","send_at"}}.
func (h *Handler) Send(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		writeError(w, err)
		return
	}
	extendDeadlines(w, h.cfg.TransferTimeout)
	form, err := h.readCompose(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()
	if form.followUpDays != 0 {
		if err := h.app.CheckFollowUp(form.followUpDays, form.sendAt); err != nil {
			writeError(w, err)
			return
		}
	}
	opts := domain.SendOptions{IdempotencyKey: key, ReplaceUID: form.replaceUID}
	if !form.sendAt.IsZero() {
		scheduled, err := h.app.Schedule(ctx, sessionFrom(r), form.draft, form.sendAt, opts)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		out := scheduleDTO{Scheduled: scheduledRefDTO{ID: scheduled.ID, SendAt: formatTime(scheduled.SendAt)}}
		out.FollowUp, out.FollowUpError = h.followUp(ctx, r, form, scheduled.MessageID, scheduled.SendAt)
		response.JSON(w, http.StatusAccepted, out)
		return
	}
	result, err := h.app.Send(ctx, sessionFrom(r), form.draft, opts)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := sendDTO{
		MessageID: result.MessageID, SavedToSent: result.SavedToSent, DraftRemoved: result.DraftRemoved, Replayed: result.Replayed,
	}
	out.FollowUp, out.FollowUpError = h.followUp(ctx, r, form, result.MessageID, time.Time{})
	response.JSON(w, http.StatusAccepted, out)
}

func (h *Handler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, h.cfg.TransferTimeout)
	form, err := h.readCompose(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	if !form.sendAt.IsZero() {
		writeError(w, domain.NewValidationError("send_at", "un borrador no se programa: se programa al enviarlo"))
		return
	}
	if form.followUpDays != 0 {
		writeError(w, domain.NewValidationError("follow_up_days", "el seguimiento se pide al enviar"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()
	uid, err := h.app.SaveDraft(ctx, sessionFrom(r), form.draft, form.replaceUID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, draftDTO{UID: uid})
}

// readCompose lee el formulario multipart parte a parte, con un tope total. No usa
// ParseMultipartForm: volcaria los ficheros grandes a un temporal en disco, y la imagen
// del servicio no tiene /tmp ni debe dejar adjuntos sin analizar en disco.
func (h *Handler) readCompose(w http.ResponseWriter, r *http.Request) (composeForm, error) {
	limit := h.cfg.MaxMessageBytes + multipartOverhead
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	mr, err := r.MultipartReader()
	if err != nil {
		return composeForm{}, domain.NewValidationError("body", "se esperaba multipart/form-data")
	}
	fields := map[string][]string{}
	var attachments []domain.Attachment
	var total int64
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return composeForm{}, bodyError(err)
		}
		name := part.FormName()
		if name == attachmentField {
			if len(attachments) >= domain.MaxAttachments {
				return composeForm{}, domain.NewValidationError("attachments", "demasiados adjuntos")
			}
			data, err := readPart(part, h.cfg.MaxMessageBytes-total)
			if err != nil {
				return composeForm{}, err
			}
			total += int64(len(data))
			attachments = append(attachments, domain.Attachment{
				Filename: part.FileName(), ContentType: part.Header.Get("Content-Type"), Data: data,
			})
			continue
		}
		if !composeFields[name] {
			return composeForm{}, domain.NewValidationError(name, "campo no admitido")
		}
		if part.FileName() != "" {
			return composeForm{}, domain.NewValidationError(name, "se esperaba texto, no un fichero")
		}
		data, err := readPart(part, h.cfg.MaxMessageBytes-total)
		if err != nil {
			return composeForm{}, err
		}
		total += int64(len(data))
		fields[name] = append(fields[name], string(data))
		if (listFields[name] && len(fields[name]) > maxListValues) || (!listFields[name] && len(fields[name]) > 1) {
			return composeForm{}, domain.NewValidationError(name, "demasiados valores")
		}
	}
	return buildComposeForm(fields, attachments)
}

func buildComposeForm(fields map[string][]string, attachments []domain.Attachment) (composeForm, error) {
	var form composeForm
	d := &form.draft
	var err error
	if v := single(fields, "from"); v != "" {
		from, err := domain.ParseAddressField("from", []string{v})
		if err != nil {
			return composeForm{}, err
		}
		if len(from) != 1 {
			return composeForm{}, domain.NewValidationError("from", "debe ser una sola dirección")
		}
		d.From = from[0]
	}
	if d.To, err = domain.ParseAddressField("to", fields["to"]); err != nil {
		return composeForm{}, err
	}
	if d.Cc, err = domain.ParseAddressField("cc", fields["cc"]); err != nil {
		return composeForm{}, err
	}
	if d.Bcc, err = domain.ParseAddressField("bcc", fields["bcc"]); err != nil {
		return composeForm{}, err
	}
	d.Subject = strings.TrimSpace(single(fields, "subject"))
	d.Text = single(fields, "text")
	d.HTML = single(fields, "html")
	d.Attachments = attachments

	if raw := single(fields, "in_reply_to"); raw != "" {
		uid, err := domain.ParseUID(raw)
		if err != nil {
			return composeForm{}, domain.NewValidationError("in_reply_to", "debe ser el UID del mensaje original")
		}
		folder := single(fields, "in_reply_to_folder")
		if folder == "" {
			folder = "INBOX"
		}
		d.InReplyTo = &domain.ReplyTarget{Folder: folder, UID: uid}
	}
	if raw := single(fields, "send_at"); raw != "" {
		at, err := domain.ParseSendAt(raw)
		if err != nil {
			return composeForm{}, err
		}
		form.sendAt = at
	}
	if raw := single(fields, "follow_up_days"); raw != "" {
		days, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || days < 1 {
			return composeForm{}, domain.NewValidationError("follow_up_days", "debe ser un número de días positivo")
		}
		form.followUpDays = days
	}
	if raw := single(fields, "replace_uid"); raw != "" {
		uid, err := domain.ParseUID(raw)
		if err != nil {
			return composeForm{}, domain.NewValidationError("replace_uid", "debe ser el UID del borrador anterior")
		}
		form.replaceUID = uid
	}
	folder, rawUID, parts := single(fields, "source_folder"), single(fields, "source_uid"), fields["source_parts"]
	if folder != "" || rawUID != "" || len(parts) > 0 {
		uid, err := domain.ParseUID(rawUID)
		if err != nil {
			return composeForm{}, domain.NewValidationError("source_uid", "debe ser el UID del mensaje de origen")
		}
		if d.Source, err = domain.NewPartSource(folder, uid, parts); err != nil {
			return composeForm{}, err
		}
	}
	return form, nil
}

func single(fields map[string][]string, name string) string {
	if v := fields[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// readPart lee una parte del formulario sin pasar de remaining bytes.
func readPart(part *multipart.Part, remaining int64) ([]byte, error) {
	if remaining < 0 {
		return nil, domain.ErrMessageTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(part, remaining+1))
	if err != nil {
		return nil, bodyError(err)
	}
	if int64(len(data)) > remaining {
		return nil, domain.ErrMessageTooLarge
	}
	return data, nil
}

func bodyError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return domain.ErrMessageTooLarge
	}
	return domain.NewValidationError("body", "formulario multipart inválido")
}
