package http

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// InternalHandler sirve en /internal/identity las operaciones sobre las cuentas de una
// empresa entera que organization orquesta al darla de alta y de baja. Solo las llaman otros
// servicios: el router exige el token interno y RequireInternalCaller rechaza cualquier
// peticion que traiga usuario. La contrasena del primer usuario entra en el cuerpo y no sale
// nunca: ni en la respuesta ni en un error.
type InternalHandler struct {
	uc *app.TenantUsersUseCase
}

func NewInternalHandler(uc *app.TenantUsersUseCase) *InternalHandler {
	return &InternalHandler{uc: uc}
}

func (h *InternalHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequireInternalCaller)
	r.Route("/tenants/{tenantID}", func(r chi.Router) {
		r.Put("/first-user", h.CreateFirstUser)
		r.Delete("/users", h.RemoveTenantUsers)
	})
	return r
}

func internalTenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "tenantID"))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant id")
		return uuid.Nil, false
	}
	return id, true
}

type firstUserRequest struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// CreateFirstUser responde 201 al crear la cuenta y 200 al repetir la misma peticion.
func (h *InternalHandler) CreateFirstUser(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := internalTenant(w, r)
	if !ok {
		return
	}
	var req firstUserRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("user_id", req.UserID)
	v.UUID("user_id", req.UserID)
	v.Required("email", req.Email)
	v.Email("email", req.Email)
	v.MaxLength("email", req.Email, 255)
	v.Required("password", req.Password)
	v.MaxLength("password", req.Password, 128)
	v.Required("first_name", req.FirstName)
	v.MaxLength("first_name", req.FirstName, 100)
	v.Required("last_name", req.LastName)
	v.MaxLength("last_name", req.LastName, 100)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	userID, _ := uuid.Parse(req.UserID)

	user, created, err := h.uc.CreateFirstUser(r.Context(), app.FirstUserRequest{
		TenantID:  tenantID,
		UserID:    userID,
		Email:     req.Email,
		Password:  req.Password,
		FirstName: req.FirstName,
		LastName:  req.LastName,
	})
	switch {
	case errors.Is(err, domain.ErrFirstUserConflict):
		response.Err(w, http.StatusConflict, "FIRST_USER_CONFLICT", "la empresa ya tiene un primer usuario distinto")
		return
	case errors.Is(err, domain.ErrPasswordPolicyFail), errors.Is(err, domain.ErrPasswordBreached):
		respondPasswordRejected(w, err)
		return
	case err != nil:
		response.Unexpected(w, fmt.Errorf("crear el primer usuario: %w", err))
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.JSON(w, status, map[string]interface{}{
		"user_id":   user.ID.String(),
		"tenant_id": user.TenantID.String(),
		"created":   created,
	})
}

func (h *InternalHandler) RemoveTenantUsers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := internalTenant(w, r)
	if !ok {
		return
	}
	removed, err := h.uc.RemoveTenantUsers(r.Context(), tenantID)
	if errors.Is(err, domain.ErrTenantActive) {
		response.Err(w, http.StatusConflict, "TENANT_ACTIVE", "las cuentas de una empresa activa no se retiran")
		return
	}
	if err != nil {
		response.Unexpected(w, fmt.Errorf("retirar las cuentas de la empresa: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]int64{"users_removed": removed})
}
