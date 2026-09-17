package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// accountRepo sirve la cuenta y un acceso vacio: lo que lee GET /access/my-modules.
type accountRepo struct {
	ports.UserRoleRepository
	account domain.UserAccount
	err     error
}

func (a *accountRepo) UserAccount(context.Context, uuid.UUID, uuid.UUID) (domain.UserAccount, error) {
	return a.account, a.err
}
func (a *accountRepo) ListRoles(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Role, error) {
	return nil, nil
}
func (a *accountRepo) ListAccessibleModules(context.Context, uuid.UUID, uuid.UUID) ([]string, error) {
	return []string{}, nil
}
func (a *accountRepo) ListWriteActionsByModule(context.Context, uuid.UUID, uuid.UUID) (map[string][]string, error) {
	return map[string][]string{}, nil
}

func myModulesServer(repo ports.UserRoleRepository) http.Handler {
	uc := app.NewRBACUseCase(nil, nil, nil, repo, nil, nil,
		app.SystemRoles{Superadmin: middleware.RoleSuperadmin, TenantAdmin: middleware.RoleTenantAdmin}, nil, zap.NewNop())
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(uc).Routes())
	return r
}

// El gateway rechaza el token solo con estas dos respuestas; cualquier otra la toma como
// "no se pudo determinar". Por eso cada caso lleva su estado y su codigo.
func TestMyModulesDistingueCuentaCerradaDeFallo(t *testing.T) {
	validFrom := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		repo   *accountRepo
		status int
		code   string
	}{
		{"activa", &accountRepo{account: domain.UserAccount{Status: "active", TokensValidFrom: validFrom}}, http.StatusOK, ""},
		{"inexistente", &accountRepo{err: domain.ErrUserNotFound}, http.StatusNotFound, "USER_NOT_FOUND"},
		{"inactiva", &accountRepo{account: domain.UserAccount{Status: "inactive"}}, http.StatusForbidden, "USER_NOT_ACTIVE"},
		{"bloqueada", &accountRepo{account: domain.UserAccount{Status: "locked"}}, http.StatusForbidden, "USER_NOT_ACTIVE"},
		{"pendiente", &accountRepo{account: domain.UserAccount{Status: "pending"}}, http.StatusForbidden, "USER_NOT_ACTIVE"},
		{"base caida", &accountRepo{err: errors.New("registro no disponible")}, http.StatusOK, ""},
	}
	for _, c := range cases {
		rec := caller{user: uuid.New(), tenant: uuid.New()}.do(t, myModulesServer(c.repo), http.MethodGet, "/api/v1/access/my-modules", "")
		if rec.Code != c.status {
			t.Errorf("%s: %d, se esperaba %d (%s)", c.name, rec.Code, c.status, rec.Body.String())
			continue
		}
		if c.code != "" {
			if got := errorCode(t, rec); got != c.code {
				t.Errorf("%s: codigo %q, se esperaba %q", c.name, got, c.code)
			}
		}
	}

	rec := caller{user: uuid.New(), tenant: uuid.New()}.do(t,
		myModulesServer(cases[0].repo), http.MethodGet, "/api/v1/access/my-modules", "")
	var payload struct {
		Data struct {
			TokensValidFrom time.Time `json:"tokens_valid_from"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || !payload.Data.TokensValidFrom.Equal(validFrom) {
		t.Fatalf("tokens_valid_from = %v (%v), se esperaba %v", payload.Data.TokensValidFrom, err, validFrom)
	}
}

// Sin empresa no hay cuenta que buscar: la peticion esta mal formada, no la cuenta cerrada.
func TestMyModulesSinEmpresaNoEsUnaCuentaCerrada(t *testing.T) {
	rec := caller{user: uuid.New()}.do(t, myModulesServer(&accountRepo{err: domain.ErrUserNotFound}),
		http.MethodGet, "/api/v1/access/my-modules", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin empresa: %d, se esperaba 401", rec.Code)
	}
}
