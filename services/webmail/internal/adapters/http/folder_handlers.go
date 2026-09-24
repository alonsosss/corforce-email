package http

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// maxBatchBody cubre MaxBatchUIDs UIDs de diez digitos y el resto del cuerpo.
const maxBatchBody = 16 << 10

type folderNameRequest struct {
	Name string `json:"name"`
}

func (h *Handler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	var req folderNameRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	f, err := h.app.CreateFolder(ctx, sessionFrom(r), req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, toFolderDTO(f))
}

func (h *Handler) RenameFolder(w http.ResponseWriter, r *http.Request) {
	folder, err := folderParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req folderNameRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	f, err := h.app.RenameFolder(ctx, sessionFrom(r), folder, req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toFolderDTO(f))
}

func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	folder, err := folderParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.DeleteFolder(ctx, sessionFrom(r), folder); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type emptyDTO struct {
	Removed int `json:"removed"`
}

func (h *Handler) EmptyFolder(w http.ResponseWriter, r *http.Request) {
	folder, err := folderParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	removed, err := h.app.EmptyFolder(ctx, sessionFrom(r), folder)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, emptyDTO{Removed: removed})
}

type batchRequest struct {
	UIDs   []uint32 `json:"uids"`
	Action string   `json:"action"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
	To     string   `json:"to"`
}

type batchDTO struct {
	Affected  int  `json:"affected"`
	Permanent bool `json:"permanent"`
}

// BatchMessages aplica una accion a varios mensajes de la carpeta: marcar, mover o borrar (a la
// papelera o, desde ella, para siempre).
func (h *Handler) BatchMessages(w http.ResponseWriter, r *http.Request) {
	folder, err := folderParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req batchRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBatchBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	batch, err := domain.NewBatch(folder, req.UIDs, req.Action, req.Add, req.Remove, req.To)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	res, err := h.app.Batch(ctx, sessionFrom(r), folder, batch)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, batchDTO{Affected: res.Affected, Permanent: res.Permanent})
}

// DownloadRaw entrega el mensaje original (.eml) como descarga, sin interpretarlo, acotado por el
// tope de descarga.
func (h *Handler) DownloadRaw(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	extendDeadlines(w, h.cfg.TransferTimeout)
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()

	started := false
	err = h.app.StreamRaw(ctx, sessionFrom(r), folder, uid, func(_ domain.StoredMessage, body io.Reader) error {
		hd := w.Header()
		hd.Set("Content-Type", "message/rfc822")
		hd.Set("Content-Disposition", contentDisposition("mensaje-"+strconv.FormatUint(uint64(uid), 10)+".eml"))
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
		h.logger.Warn("webmail: descarga del original interrumpida", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		panic(http.ErrAbortHandler)
	}
	h.fail(w, r, err)
}
