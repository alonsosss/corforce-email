package mailauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *tls.Config) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func newClient(t *testing.T, cfg Config, resolver CellResolver) *Client {
	t.Helper()
	cfg.Timeout = 5 * time.Second
	c, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func okBody(tenant, mailbox uuid.UUID, username string) string {
	b, _ := json.Marshal(map[string]any{"success": true, "username": username, "tenant_id": tenant, "mailbox_id": mailbox})
	return string(b)
}

func TestAutenticaConElContratoDeDovecotConServicioDav(t *testing.T) {
	tenant, mailbox := uuid.New(), uuid.New()
	var got map[string]string
	srv, tlsCfg := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(okBody(tenant, mailbox, "ana@empresa.pe")))
	})
	p, err := newClient(t, Config{BaseURL: srv.URL, TLS: tlsCfg}, nil).Authenticate(context.Background(), " Ana@Empresa.PE ", "s3cr3t", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if p.TenantID != tenant || p.MailboxID != mailbox || p.Username != "ana@empresa.pe" {
		t.Fatalf("identidad: %+v", p)
	}
	for k, v := range map[string]string{"username": "ana@empresa.pe", "password": "s3cr3t", "real_rip": "203.0.113.7", "service": "dav"} {
		if got[k] != v {
			t.Fatalf("campo %s = %q, se esperaba %q", k, got[k], v)
		}
	}
}

func TestLosRechazosSonCredencialesInvalidas(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"401":           {http.StatusUnauthorized, `{"success":false}`},
		"200 sin exito": {http.StatusOK, `{"success":false}`},
	} {
		srv, tlsCfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		})
		if _, err := newClient(t, Config{BaseURL: srv.URL, TLS: tlsCfg}, nil).Authenticate(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLosFallosDeMailAuthSonNoDisponible(t *testing.T) {
	tenant, mailbox := uuid.New(), uuid.New()
	for name, body := range map[string]struct {
		status int
		body   string
	}{
		"500":                   {500, ``},
		"503":                   {503, ``},
		"429":                   {429, ``},
		"respuesta ilegible":    {200, `no es json`},
		"identidad incompleta":  {200, `{"success":true,"username":"ana@empresa.pe"}`},
		"empresa vacia":         {200, `{"success":true,"username":"ana@empresa.pe","tenant_id":"` + uuid.Nil.String() + `","mailbox_id":"` + mailbox.String() + `"}`},
		"identificador roto":    {200, `{"success":true,"username":"ana@empresa.pe","tenant_id":"x","mailbox_id":"y"}`},
		"otro buzon respondido": {200, okBody(tenant, mailbox, "bea@empresa.pe")},
	} {
		srv, tlsCfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(body.status)
			_, _ = w.Write([]byte(body.body))
		})
		if _, err := newClient(t, Config{BaseURL: srv.URL, TLS: tlsCfg}, nil).Authenticate(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := newClient(t, Config{BaseURL: "https://127.0.0.1:1"}, nil).Authenticate(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("mail-auth inalcanzable: %v", err)
	}
}

func TestNoSeSiguenRedireccionesConLaContrasena(t *testing.T) {
	var reached atomic.Bool
	target, tlsCfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	redirector, _ := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := newClient(t, Config{BaseURL: redirector.URL, TLS: tlsCfg}, nil).Authenticate(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("una redireccion es un fallo: %v", err)
	}
	if reached.Load() {
		t.Fatal("la contrasena llego al destino de la redireccion")
	}
}

func TestSoloConexionesVerificadas(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	// Sin la CA del servidor, el certificado no se verifica y no se envia nada.
	if _, err := newClient(t, Config{BaseURL: srv.URL, TLS: &tls.Config{MinVersion: tls.VersionTLS12}}, nil).Authenticate(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("certificado no verificable: %v", err)
	}
	for _, bad := range []string{"http://mail-auth:9082", "mail-auth:9082", "https://", "https://ana:clave@mail-auth:9082", "https://mail-auth:9082/?x=1", ""} {
		if _, err := New(Config{BaseURL: bad}, nil); err == nil {
			t.Errorf("%q debia rechazarse", bad)
		}
	}
}

func TestEntradasQueNoLlegaAMailAuth(t *testing.T) {
	var calls atomic.Int32
	srv, tlsCfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) })
	c := newClient(t, Config{BaseURL: srv.URL, TLS: tlsCfg}, nil)
	for _, in := range [][3]string{{"", "x", "1.1.1.1"}, {"sin-dominio", "x", "1.1.1.1"}, {"ana@empresa.pe", "", "1.1.1.1"}, {"ana@empresa.pe", "x", ""}, {"a b@empresa.pe", "x", "1.1.1.1"}} {
		if _, err := c.Authenticate(context.Background(), in[0], in[1], in[2]); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("%q: %v", in, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("una entrada mal formada no debe consultar a mail-auth")
	}
}

type fakeResolver struct {
	cells map[string]string
	err   error
	asked []string
}

func (f *fakeResolver) CellOf(_ context.Context, mailDomain string) (string, error) {
	f.asked = append(f.asked, mailDomain)
	if f.err != nil {
		return "", f.err
	}
	cell, ok := f.cells[mailDomain]
	if !ok {
		return "", tenantcell.ErrUnknownDomain
	}
	return cell, nil
}

func TestConVariasCeldasVaAlMailAuthDeLaCeldaDelDominio(t *testing.T) {
	tenant, mailbox := uuid.New(), uuid.New()
	var hits [3]atomic.Int32
	mk := func(i int) (*httptest.Server, *tls.Config) {
		return tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
			hits[i].Add(1)
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			_, _ = w.Write([]byte(okBody(tenant, mailbox, req["username"])))
		})
	}
	base, tlsCfg := mk(0)
	segunda, _ := mk(1)
	tercera, _ := mk(2)
	res := &fakeResolver{cells: map[string]string{"uno.pe": "pe-01", "dos.pe": "pe-02"}}
	c := newClient(t, Config{BaseURL: base.URL, BaseCell: "pe-01", CellURLs: map[string]string{"pe-02": segunda.URL, "pe-03": tercera.URL}, TLS: tlsCfg}, res)

	for user, want := range map[string]int{"ana@uno.pe": 0, "bea@dos.pe": 1, "eva@sin-registro.pe": 0} {
		before := [3]int32{hits[0].Load(), hits[1].Load(), hits[2].Load()}
		if _, err := c.Authenticate(context.Background(), user, "x", "203.0.113.7"); err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		for i := range hits {
			delta := hits[i].Load() - before[i]
			if (i == want) != (delta == 1) || (i != want && delta != 0) {
				t.Errorf("%s: la celda %d recibio %d llamadas", user, i, delta)
			}
		}
	}

	res.cells["cel@tres.pe"] = "pe-99"
	res.cells["tres.pe"] = "pe-99"
	if _, err := c.Authenticate(context.Background(), "x@tres.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("celda sin mail-auth declarado: %v", err)
	}
	res.err = tenantcell.ErrUnresolved
	if _, err := c.Authenticate(context.Background(), "ana@uno.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("organization sin respuesta no adivina la celda: %v", err)
	}
}

func TestConfiguracionDeVariasCeldas(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://a:1", CellURLs: map[string]string{"pe-02": "https://b:1"}}, &fakeResolver{}); err == nil {
		t.Error("sin celda base no se sabe que sirve BaseURL")
	}
	if _, err := New(Config{BaseURL: "https://a:1", BaseCell: "pe-01", CellURLs: map[string]string{"pe-02": "https://b:1"}}, nil); err == nil {
		t.Error("sin resolvedor no hay forma de elegir celda")
	}
	if _, err := New(Config{BaseURL: "https://a:1", BaseCell: "pe-01", CellURLs: map[string]string{"pe-01": "https://b:1"}}, &fakeResolver{}); err == nil {
		t.Error("la celda base no se declara tambien como instancia")
	}
	if _, err := New(Config{BaseURL: "https://a:1", BaseCell: "pe-01", CellURLs: map[string]string{"MAL": "https://b:1"}}, &fakeResolver{}); err == nil {
		t.Error("codigo de celda invalido")
	}
	if _, err := New(Config{BaseURL: "https://a:1", BaseCell: "pe-01", CellURLs: map[string]string{"pe-02": "http://b:1"}}, &fakeResolver{}); err == nil {
		t.Error("las celdas tambien exigen https")
	}
}
