package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fpLookups cuenta todo lo que dependeria de la cuenta: la peticion no debe llegar a nada de ello.
type fpLookups struct{ n int }

type fpUsers struct {
	ports.UserRepository
	seen *fpLookups
}

func (s fpUsers) GetByEmail(context.Context, uuid.UUID, string) (*domain.User, error) {
	s.seen.n++
	return nil, domain.ErrUserNotFound
}

type fpTenants struct {
	ports.TenantRepository
	seen *fpLookups
}

func (t fpTenants) GetIDByEmail(context.Context, string) (uuid.UUID, error) {
	t.seen.n++
	return uuid.Nil, domain.ErrTenantNotFound
}

type fpResets struct {
	ports.PasswordResetRepository
	seen *fpLookups
}

func (r fpResets) Create(context.Context, *domain.PasswordResetToken) error {
	r.seen.n++
	return nil
}
func (r fpResets) InvalidateForUser(context.Context, uuid.UUID) error { r.seen.n++; return nil }

type fpMailer struct{ seen *fpLookups }

func (m fpMailer) Send(context.Context, uuid.UUID, ports.OutgoingMail) error {
	m.seen.n++
	return nil
}
func (m fpMailer) Configured() bool { return true }

type fpQueue struct{ got []ports.PasswordResetRequest }

func (q *fpQueue) Enqueue(req ports.PasswordResetRequest) bool {
	q.got = append(q.got, req)
	return true
}

// "Olvide mi contrasena" responde el mismo estado y el mismo cuerpo a un correo registrado y a
// uno desconocido, y en los dos casos solo encola: ninguna busqueda, enlace ni envio ocurre antes
// de la respuesta, asi que su tiempo tampoco depende de la cuenta. Un correo mal formado se
// rechaza sin encolar nada.
func TestForgotPasswordRespondeIgualExistaONoLaCuenta(t *testing.T) {
	seen := &fpLookups{}
	queue := &fpQueue{}
	resetUC, err := app.NewPasswordResetUseCase(app.PasswordResetDeps{
		Users: fpUsers{seen: seen}, Tenants: fpTenants{seen: seen}, Resets: fpResets{seen: seen},
		Mailer: fpMailer{seen: seen}, Queue: queue, Logger: zap.NewNop(), PublicBaseURL: "https://app.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(nil, nil, resetUC, authz.NewChecker(unreachable, ""), Config{}).Routes())
	f := &lgFixture{srv: r}

	known := f.post(t, "/api/v1/auth/forgot-password", map[string]string{"email": "ana@example.test"}, nil)
	unknown := f.post(t, "/api/v1/auth/forgot-password", map[string]string{"email": "nadie@example.test"}, nil)
	if known.Code != http.StatusOK || unknown.Code != known.Code || unknown.Body.String() != known.Body.String() {
		t.Fatalf("registrado %d %q, desconocido %d %q", known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
	if len(queue.got) != 2 || queue.got[0].Email != "ana@example.test" || queue.got[1].Email != "nadie@example.test" {
		t.Fatalf("encolado %+v, se esperaba una solicitud de cada correo", queue.got)
	}
	if seen.n != 0 {
		t.Fatalf("la peticion hizo %d operaciones que dependen de la cuenta", seen.n)
	}

	bad := f.post(t, "/api/v1/auth/forgot-password", map[string]string{"email": "no-es-un-correo"}, nil)
	if bad.Code != http.StatusUnprocessableEntity || len(queue.got) != 2 {
		t.Fatalf("correo mal formado: %d, %d en cola", bad.Code, len(queue.got))
	}
}
