package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type threadInfoDTO struct {
	Size         int          `json:"size"`
	Unread       int          `json:"unread"`
	UIDs         []uint32     `json:"uids"`
	Participants []addressDTO `json:"participants"`
}

// threadRowDTO es una fila del listado por conversaciones: el sobre del ultimo mensaje, igual que
// una fila por mensajes, con el resumen de la conversacion.
type threadRowDTO struct {
	envelopeDTO
	Thread threadInfoDTO `json:"thread"`
}

func toThreadRowDTOs(items []domain.ThreadSummary) []threadRowDTO {
	out := make([]threadRowDTO, len(items))
	for i, t := range items {
		uids := t.UIDs
		if uids == nil {
			uids = []uint32{}
		}
		out[i] = threadRowDTO{
			envelopeDTO: toEnvelopeDTO(t.Latest),
			Thread:      threadInfoDTO{Size: t.Size, Unread: t.Unread, UIDs: uids, Participants: toAddressDTOs(t.Participants)},
		}
	}
	return out
}

type conversationMessageDTO struct {
	envelopeDTO
	Folder    string `json:"folder"`
	MessageID string `json:"message_id"`
}

// listThreads atiende GET /folders/{folder}/messages?view=threads con los mismos filtros y la misma
// paginacion (ahora de conversaciones) que la vista por mensajes.
func (h *Handler) listThreads(w http.ResponseWriter, r *http.Request, folder string, query domain.ListQuery) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	result, err := h.app.ListThreads(ctx, sessionFrom(r), folder, query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, toThreadRowDTOs(result.Items),
		response.PageMetaCapped(int64(result.Total), result.Capped, query.Page, query.PerPage))
}

// messageQuery lee ?folder= y ?uid= de las rutas de la ficha y de la conversacion.
func messageQuery(r *http.Request) (string, uint32, error) {
	q := r.URL.Query()
	folder := q.Get("folder")
	if err := domain.ValidateFolderName(folder); err != nil {
		return "", 0, err
	}
	uid, err := domain.ParseUID(q.Get("uid"))
	if err != nil {
		return "", 0, err
	}
	return folder, uid, nil
}

// Conversation atiende GET /threads?folder=&uid=: la conversacion del mensaje, de la mas antigua a
// la mas reciente, con las respuestas propias de Enviados.
func (h *Handler) Conversation(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	msgs, err := h.app.Conversation(ctx, sessionFrom(r), folder, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]conversationMessageDTO, len(msgs))
	for i, m := range msgs {
		out[i] = conversationMessageDTO{envelopeDTO: toEnvelopeDTO(m.Envelope), Folder: m.Folder, MessageID: m.MessageID}
	}
	response.JSON(w, http.StatusOK, out)
}

type authenticationDTO struct {
	SPF   *string `json:"spf"`
	DKIM  *string `json:"dkim"`
	DMARC *string `json:"dmarc"`
}

type shieldReasonDTO struct {
	Code   string            `json:"code"`
	Level  string            `json:"level"`
	Params map[string]string `json:"params"`
}

type shieldDTO struct {
	Level          string            `json:"level"`
	External       bool              `json:"external"`
	Partial        bool              `json:"partial"`
	Authentication authenticationDTO `json:"authentication"`
	Reasons        []shieldReasonDTO `json:"reasons"`
}

type unsubscribeDTO struct {
	Method *string `json:"method"`
	// Target es el servidor (one_click) o la direccion (mailto) que recibe la baja.
	Target string `json:"target"`
	// URL solo en web: la pagina que el usuario abre por su cuenta.
	URL string `json:"url,omitempty"`
}

type senderInsightDTO struct {
	Sender      *addressDTO    `json:"sender"`
	Category    string         `json:"category"`
	Shield      shieldDTO      `json:"shield"`
	Unsubscribe unsubscribeDTO `json:"unsubscribe"`
}

func verdict(v domain.AuthVerdict) *string {
	if v == "" {
		return nil
	}
	s := string(v)
	return &s
}

func toSenderInsightDTO(in domain.SenderInsight) senderInsightDTO {
	reasons := make([]shieldReasonDTO, len(in.Shield.Reasons))
	for i, r := range in.Shield.Reasons {
		params := r.Params
		if params == nil {
			params = map[string]string{}
		}
		reasons[i] = shieldReasonDTO{Code: r.Code, Level: string(r.Level), Params: params}
	}
	out := senderInsightDTO{
		Category: string(in.Category),
		Shield: shieldDTO{
			Level: string(in.Shield.Level), External: in.Shield.External, Partial: in.Shield.Partial, Reasons: reasons,
			Authentication: authenticationDTO{
				SPF: verdict(in.Shield.Auth.SPF), DKIM: verdict(in.Shield.Auth.DKIM), DMARC: verdict(in.Shield.Auth.DMARC),
			},
		},
	}
	if in.Sender != nil {
		out.Sender = &addressDTO{Name: in.Sender.Name, Email: in.Sender.Email}
	}
	if in.Unsubscribe.Method != "" {
		method := string(in.Unsubscribe.Method)
		out.Unsubscribe.Method = &method
		switch in.Unsubscribe.Method {
		case domain.UnsubscribeOneClick:
			out.Unsubscribe.Target = in.Unsubscribe.Host()
		case domain.UnsubscribeMailto:
			out.Unsubscribe.Target = in.Unsubscribe.Mailto.Address.Email
		case domain.UnsubscribeWeb:
			out.Unsubscribe.Target = in.Unsubscribe.Host()
			out.Unsubscribe.URL = in.Unsubscribe.URL
		}
	}
	return out
}

// SenderInsight atiende GET /sender-insight?folder=&uid=: pestana, escudo antifraude y baja del
// mensaje. No lo marca como leido.
func (h *Handler) SenderInsight(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	insight, err := h.app.SenderInsight(ctx, sessionFrom(r), folder, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toSenderInsightDTO(insight))
}

type unsubscribeRequest struct {
	Folder string `json:"folder"`
	UID    uint32 `json:"uid"`
}

type unsubscribeResultDTO struct {
	Method string `json:"method"`
	Target string `json:"target"`
}

// Unsubscribe atiende POST /unsubscribe: la baja la decide el mensaje guardado, no el cuerpo, que
// solo dice cual es.
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	var req unsubscribeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if err := domain.ValidateFolderName(req.Folder); err != nil {
		writeError(w, err)
		return
	}
	if req.UID == 0 {
		writeError(w, domain.NewValidationError("uid", "debe ser un entero positivo"))
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	res, err := h.app.Unsubscribe(ctx, sessionFrom(r), req.Folder, req.UID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toUnsubscribeResultDTO(res))
}

func toUnsubscribeResultDTO(res app.UnsubscribeResult) unsubscribeResultDTO {
	return unsubscribeResultDTO{Method: string(res.Method), Target: res.Target}
}
