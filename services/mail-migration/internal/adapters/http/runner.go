package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// maxRunnerBody: un latido lleva como mucho MaxFolders carpetas de nombre acotado.
	maxRunnerBody = 256 << 10

	codeLeaseLost     = "LEASE_LOST"
	codeUnknownTenant = "TENANT_NOT_FOUND"
)

// RunnerHandler es la API del ejecutor mail-migration-runner. No pasa por el gateway ni lleva sesion:
// se autentica con su propia clave de servicio y sirve en su propio listener.
type RunnerHandler struct {
	uc      *app.UseCase
	keyHash [sha256.Size]byte
	logger  *zap.Logger
}

func NewRunnerHandler(uc *app.UseCase, apiKey string, logger *zap.Logger) *RunnerHandler {
	return &RunnerHandler{uc: uc, keyHash: sha256.Sum256([]byte(apiKey)), logger: logger}
}

func (h *RunnerHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(h.authenticate)
	r.Post("/v1/claim", h.Claim)
	r.Post("/v1/tenants/{tenant_id}/jobs/{job_id}/heartbeat", h.Heartbeat)
	r.Post("/v1/tenants/{tenant_id}/jobs/{job_id}/complete", h.Complete)
	return r
}

// authenticate compara la clave en tiempo constante, sobre su huella para no filtrar el largo.
func (h *RunnerHandler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(got[:], h.keyHash[:]) != 1 {
			h.logger.Warn("peticion del ejecutor sin clave valida", zap.String("remote", r.RemoteAddr), zap.String("path", r.URL.Path))
			response.ErrUnauthorized(w, "no autorizado")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type claimRequest struct {
	RunnerID string `json:"runner_id"`
}

type claimSourceDTO struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type claimDestinationDTO struct {
	Username string `json:"username"`
	// Password es la credencial de destino del trabajo; ausente cuando el servicio no las emite.
	Password string `json:"password,omitempty"`
}

type claimDTO struct {
	JobID        string              `json:"job_id"`
	TenantID     string              `json:"tenant_id"`
	LeaseID      string              `json:"lease_id"`
	Attempt      int                 `json:"attempt"`
	Source       claimSourceDTO      `json:"source"`
	Destination  claimDestinationDTO `json:"destination"`
	LeaseSeconds int                 `json:"lease_seconds"`
}

// Claim es la unica respuesta del servicio que lleva la contrasena de origen y la credencial de destino en claro.
func (h *RunnerHandler) Claim(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRunnerBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	job, err := h.uc.Claim(r.Context(), req.RunnerID)
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response.JSON(w, http.StatusOK, claimDTO{
		JobID: job.JobID.String(), TenantID: job.TenantID.String(), LeaseID: job.LeaseID.String(), Attempt: job.Attempt,
		Source: claimSourceDTO{
			Host: job.SourceHost, Port: job.SourcePort, TLS: string(job.SourceTLS),
			Username: job.SourceUsername, Password: job.SourcePassword,
		},
		Destination:  claimDestinationDTO{Username: job.DestinationUsername, Password: job.DestinationPassword},
		LeaseSeconds: job.LeaseSeconds,
	})
}

type heartbeatRequest struct {
	LeaseID  string      `json:"lease_id"`
	Phase    string      `json:"phase"`
	Progress progressDTO `json:"progress"`
}

func (h *RunnerHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	tenantID, jobID, ok := jobIDs(w, r)
	if !ok {
		return
	}
	var req heartbeatRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRunnerBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	leaseID, err := uuid.Parse(req.LeaseID)
	if err != nil {
		response.ErrBadRequest(w, "lease_id no es un identificador")
		return
	}
	res, err := h.uc.Heartbeat(r.Context(), tenantID, jobID, app.HeartbeatInput{LeaseID: leaseID, Phase: req.Phase, Progress: req.Progress.toDomain()})
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{"cancel": res.Cancel, "lease_seconds": res.LeaseSeconds})
}

type completeRequest struct {
	LeaseID  string      `json:"lease_id"`
	Outcome  string      `json:"outcome"`
	Progress progressDTO `json:"progress"`
	Error    *errorDTO   `json:"error"`
}

func (h *RunnerHandler) Complete(w http.ResponseWriter, r *http.Request) {
	tenantID, jobID, ok := jobIDs(w, r)
	if !ok {
		return
	}
	var req completeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRunnerBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	leaseID, err := uuid.Parse(req.LeaseID)
	if err != nil {
		response.ErrBadRequest(w, "lease_id no es un identificador")
		return
	}
	in := app.CompleteInput{LeaseID: leaseID, Outcome: req.Outcome, Progress: req.Progress.toDomain()}
	if req.Error != nil {
		in.ErrorCode, in.ErrorMessage = req.Error.Code, req.Error.Message
	}
	job, err := h.uc.Complete(r.Context(), tenantID, jobID, in)
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toJobDTO(job))
}

func (p progressDTO) toDomain() domain.Progress {
	folders := make([]domain.FolderProgress, len(p.Folders))
	for i, f := range p.Folders {
		folders[i] = domain.FolderProgress(f)
	}
	return domain.Progress{
		FoldersTotal: p.FoldersTotal, FoldersDone: p.FoldersDone, MessagesTotal: p.MessagesTotal,
		MessagesCopied: p.MessagesCopied, MessagesSkipped: p.MessagesSkipped, MessagesFailed: p.MessagesFailed,
		BytesCopied: p.BytesCopied, Folders: folders,
	}
}

func jobIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		response.ErrBadRequest(w, "tenant_id no es un identificador")
		return uuid.Nil, uuid.Nil, false
	}
	jobID, err := uuid.Parse(chi.URLParam(r, "job_id"))
	if err != nil {
		response.ErrBadRequest(w, "job_id no es un identificador")
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, jobID, true
}

func writeRunnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrLeaseLost):
		response.Err(w, http.StatusConflict, codeLeaseLost, err.Error())
	case errors.Is(err, domain.ErrTenantUnknown):
		response.Err(w, http.StatusNotFound, codeUnknownTenant, err.Error())
	case errors.Is(err, domain.ErrNotConfigured):
		response.Err(w, http.StatusServiceUnavailable, codeNotConfigured, err.Error())
	case errors.Is(err, domain.ErrInvalidOutcome), errors.Is(err, domain.ErrInvalidProgress),
		errors.Is(err, domain.ErrInvalidPhase), errors.Is(err, domain.ErrInvalidRunner):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}
