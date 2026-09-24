package http

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	maxSmallBody = 4 << 10
	// partCSP acompana a cada adjunto: aunque el navegador lo abriera, no ejecuta nada.
	partCSP = "default-src 'none'; sandbox"
)

// folderParam lee el nombre de carpeta de la ruta. El cliente lo envia codificado (una
// subcarpeta lleva el separador "/" como %2F); cuando hay escapes chi enruta sobre la
// ruta cruda y el parametro llega sin decodificar.
func folderParam(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "folder")
	if r.URL.RawPath != "" {
		decoded, err := url.PathUnescape(raw)
		if err != nil {
			return "", domain.NewValidationError("folder", "codificacion invalida")
		}
		raw = decoded
	}
	if err := domain.ValidateFolderName(raw); err != nil {
		return "", err
	}
	return raw, nil
}

func messageParams(r *http.Request) (string, uint32, error) {
	folder, err := folderParam(r)
	if err != nil {
		return "", 0, err
	}
	uid, err := domain.ParseUID(chi.URLParam(r, "uid"))
	if err != nil {
		return "", 0, err
	}
	return folder, uid, nil
}

func (h *Handler) Folders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	folders, err := h.app.Folders(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toFolderDTOs(folders))
}

func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	folder, err := folderParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	page, err := optionalInt(q.Get("page"), "page")
	if err != nil {
		writeError(w, err)
		return
	}
	perPage, err := optionalInt(q.Get("per_page"), "per_page")
	if err != nil {
		writeError(w, err)
		return
	}
	query, err := domain.NewListQuery(page, perPage, q.Get("search"))
	if err != nil {
		writeError(w, err)
		return
	}
	filter, err := searchFilter(q)
	if err != nil {
		writeError(w, err)
		return
	}
	if query, err = query.WithFilter(filter); err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	result, err := h.app.ListMessages(ctx, sessionFrom(r), folder, query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, toEnvelopeDTOs(result.Items),
		response.PageMetaCapped(int64(result.Total), result.Capped, query.Page, query.PerPage))
}

// searchFilter lee los criterios de la busqueda avanzada de la query.
func searchFilter(q url.Values) (domain.SearchFilter, error) {
	f := domain.SearchFilter{From: q.Get("from"), To: q.Get("to"), Subject: q.Get("subject")}
	var err error
	if f.Since, err = domain.ParseSearchDate("since", q.Get("since")); err != nil {
		return f, err
	}
	if f.Before, err = domain.ParseSearchDate("before", q.Get("before")); err != nil {
		return f, err
	}
	for _, flag := range []struct {
		name string
		dst  *bool
	}{{"unread", &f.Unread}, {"flagged", &f.Flagged}, {"has_attachments", &f.HasAttachments}} {
		if *flag.dst, err = optionalBool(q.Get(flag.name), flag.name); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (h *Handler) ReadMessage(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	peek := q.Get("peek") == "true"
	allowRemote := q.Get("remote_images") == "allow"
	ctx, cancel := h.opContext(r)
	defer cancel()
	msg, err := h.app.ReadMessage(ctx, sessionFrom(r), folder, uid, peek, allowRemote)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toMessageDTO(msg))
}

// DownloadPart entrega un adjunto o una imagen en linea: siempre como descarga, con
// nombre saneado y con un tipo inofensivo. Nunca se renderiza como documento.
func (h *Handler) DownloadPart(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	partID := chi.URLParam(r, "part")
	extendDeadlines(w, h.cfg.TransferTimeout)
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()

	started := false
	err = h.app.StreamPart(ctx, sessionFrom(r), folder, uid, partID, func(p domain.Part, body io.Reader) error {
		hd := w.Header()
		hd.Set("Content-Type", domain.SafeDownloadType(p.ContentType))
		hd.Set("Content-Disposition", contentDisposition(p.Filename))
		hd.Set("Content-Security-Policy", partCSP)
		hd.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		started = true
		_, err := io.Copy(w, body)
		return err
	})
	if err == nil {
		return
	}
	if started {
		// Las cabeceras ya salieron: se corta la conexion para que el cliente no tome
		// por completo un fichero truncado.
		h.logger.Warn("webmail: descarga interrumpida", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		panic(http.ErrAbortHandler)
	}
	h.fail(w, r, err)
}

func contentDisposition(filename string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": domain.SanitizeFilename(filename)}); v != "" {
		return v
	}
	return "attachment"
}

type flagsRequest struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func (h *Handler) ChangeFlags(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req flagsRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	change, err := domain.NewFlagChange(req.Add, req.Remove)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.ChangeFlags(ctx, sessionFrom(r), folder, uid, change); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type moveRequest struct {
	To string `json:"to"`
}

func (h *Handler) MoveMessage(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req moveRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.Move(ctx, sessionFrom(r), folder, uid, req.To); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	permanent, err := h.app.Delete(ctx, sessionFrom(r), folder, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, deleteDTO{Permanent: permanent})
}

func optionalBool(raw, field string) (bool, error) {
	switch raw {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, domain.NewValidationError(field, "debe ser true o false")
}

func optionalInt(raw, field string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, domain.NewValidationError(field, "debe ser un entero")
	}
	return v, nil
}
