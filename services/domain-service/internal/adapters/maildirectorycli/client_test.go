package maildirectorycli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type recibido struct {
	metodo, ruta, empresa, cuerpo string
}

// nuevo monta el cliente contra una instancia de una celda que responde status y cuerpo, y
// devuelve lo que la instancia recibio.
func nuevo(t *testing.T, status int, cuerpo string) (*Client, func() []recibido) {
	t.Helper()
	var mu sync.Mutex
	var got []recibido
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recibido{r.Method, r.URL.EscapedPath(), r.Header.Get("X-Tenant-ID"), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, cuerpo)
	}))
	t.Cleanup(srv.Close)
	targets, err := tenantcell.Instances{ByEnv: map[string]map[string]string{"X": {}}}.Targets("X", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(tenantcell.NewCaller("mail-directory", targets, "token-interno", zap.NewNop(), tenantcell.CallerOptions{})), func() []recibido {
		mu.Lock()
		defer mu.Unlock()
		return append([]recibido(nil), got...)
	}
}

func TestActivarLlevaLaEmpresaYElEstado(t *testing.T) {
	c, recibidos := nuevo(t, http.StatusOK, `{"data":{"active":true}}`)
	tenant := uuid.New()
	if err := c.SetActivation(context.Background(), tenant, "acme.test", true); err != nil {
		t.Fatal(err)
	}
	want := recibido{http.MethodPut, "/internal/mail-directory/domains/acme.test/activation", tenant.String(), `{"active":true}`}
	if got := recibidos(); len(got) != 1 || got[0] != want {
		t.Errorf("recibido %+v, se esperaba %+v", got, want)
	}
}

func TestConBuzonesNoSeDesactiva(t *testing.T) {
	c, _ := nuevo(t, http.StatusConflict, `{"error":{"code":"CONFLICT","message":"buzones"}}`)
	if err := c.SetActivation(context.Background(), uuid.New(), "acme.test", false); !errors.Is(err, domain.ErrDomainHasMailboxes) {
		t.Errorf("409: %v", err)
	}
}

// Una empresa dada de baja en la celda no activa nada: su 409 no es un dominio con buzones.
func TestUnaEmpresaDadaDeBajaNoSeActiva(t *testing.T) {
	c, _ := nuevo(t, http.StatusConflict, `{"error":{"code":"TENANT_RETIRED","message":"x"}}`)
	for _, active := range []bool{true, false} {
		err := c.SetActivation(context.Background(), uuid.New(), "acme.test", active)
		if !errors.Is(err, domain.ErrTenantBeingRemoved) || errors.Is(err, domain.ErrDomainHasMailboxes) {
			t.Errorf("active=%v: %v", active, err)
		}
	}
}

// mail-directory da de alta el dominio que no tiene: un 404, con o sin sobre, es una instancia
// que no sirve la ruta y la desactivacion no esta hecha.
func TestUn404NoEsUnaDesactivacionHecha(t *testing.T) {
	for nombre, cuerpo := range map[string]string{"sin sobre": "404 page not found", "con sobre": `{"error":{"code":"NOT_FOUND","message":"x"}}`} {
		c, _ := nuevo(t, http.StatusNotFound, cuerpo)
		if err := c.SetActivation(context.Background(), uuid.New(), "acme.test", false); err == nil {
			t.Errorf("%s: un 404 se dio por desactivado", nombre)
		}
	}
}

func TestInstanciaDeOtraCeldaEsErrorDeConfiguracion(t *testing.T) {
	c, _ := nuevo(t, http.StatusForbidden, `{"error":{"code":"TENANT_NOT_IN_CELL","message":"x"}}`)
	for _, active := range []bool{true, false} {
		err := c.SetActivation(context.Background(), uuid.New(), "acme.test", active)
		if !errors.Is(err, tenantcell.ErrNotInCell) || errors.Is(err, domain.ErrDomainHasMailboxes) {
			t.Errorf("active=%v: %v", active, err)
		}
	}
}

func TestOtroErrorNoEsHecho(t *testing.T) {
	c, _ := nuevo(t, http.StatusInternalServerError, `{"error":{"code":"INTERNAL_ERROR","message":"x"}}`)
	if err := c.SetActivation(context.Background(), uuid.New(), "acme.test", true); err == nil {
		t.Error("un 500 se dio por activado")
	}
}
