package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

const (
	testLinkKey = "clave-de-firma-de-pruebas-con-mas-de-32-caracteres"
	testCell    = "pe-01"
)

type linkServer struct {
	srv   *httptest.Server
	q     *apptest.Quarantine
	links *domain.QuarantineLinkSigner
	item  domain.QuarantineItem
}

func newLinkServer(t *testing.T) *linkServer {
	t.Helper()
	links, err := domain.NewQuarantineLinkSigner(testLinkKey, "https://app.example.com", testCell, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	q := &apptest.Quarantine{}
	notices := apptest.NewNotices(q)
	uc := app.NewQuarantineUseCase(app.QuarantineDeps{Tx: &apptest.Tx{Snapshot: notices.Snapshot}, Repo: q, Reinjector: noopReinjector{},
		Events: &apptest.Publisher{}, Notices: notices, Links: links, Logger: zap.NewNop()})
	id := uuid.New()
	sum := sha256.Sum256([]byte(id.String()))
	item := domain.QuarantineItem{ID: id, TenantID: uuid.New(), Rcpt: "ana@acme.com", Score: decimal.NewFromInt(7),
		Subject: `<script>alert(1)</script>Factura`, Sender: `"><img src=x>@evil.test`, QHash: hex.EncodeToString(sum[:]), Msg: []byte("m")}
	q.Items = []domain.QuarantineItem{item}
	srv := httptest.NewServer(NewHandler(nil, uc, nil, authz.NewCheckerFromEnv()).Routes())
	t.Cleanup(srv.Close)
	return &linkServer{srv: srv, q: q, links: links, item: item}
}

// path devuelve ruta y consulta del enlace, como llegan al servicio tras el gateway.
func (s *linkServer) path(action domain.QuarantineLinkAction, expires time.Time) string {
	raw := s.links.URL(domain.QuarantineLinkClaims{TenantID: s.item.TenantID, MessageID: s.item.ID, Action: action, ExpiresAt: expires.Unix()}, s.item.QHash)
	u, _ := url.Parse(raw)
	return u.RequestURI()
}

// legacyPath es un enlace sin celda de los emitidos antes de llevarla en la ruta.
func (s *linkServer) legacyPath(action domain.QuarantineLinkAction, expires time.Time) string {
	claims := domain.QuarantineLinkClaims{TenantID: s.item.TenantID, MessageID: s.item.ID, Action: action, ExpiresAt: expires.Unix()}
	q := url.Values{}
	q.Set("t", claims.TenantID.String())
	q.Set("q", s.item.QHash)
	q.Set("e", strconv.FormatInt(claims.ExpiresAt, 10))
	q.Set("sig", apptest.LegacyLinkSignature(testLinkKey, claims))
	return domain.QuarantineLinkBasePath + "/" + string(action) + "?" + q.Encode()
}

func do(t *testing.T, method, target string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, target, nil)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

type noopReinjector struct{}

func (noopReinjector) Reinject(context.Context, string, string, []byte) error { return nil }

func TestPaginaDelEnlaceSinJavaScriptYConTextoEscapado(t *testing.T) {
	s := newLinkServer(t)
	path := s.path(domain.LinkRelease, time.Now().Add(30*time.Minute))
	status, body := do(t, http.MethodGet, s.srv.URL+path)
	if status != http.StatusOK || !strings.Contains(body, "Liberar mensaje") {
		t.Fatalf("pagina: %d %s", status, body)
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "<img") {
		t.Fatalf("contenido hostil o JavaScript en la pagina:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;Factura") || !strings.Contains(body, `method="post"`) ||
		!strings.Contains(body, strings.ReplaceAll(path, "&", "&amp;")) {
		t.Fatalf("asunto escapado y formulario a la misma URL:\n%s", body)
	}
	if len(s.q.Items) != 1 {
		t.Fatal("abrir el enlace no libera nada")
	}
}

// Firma alterada, caducado, mensaje desconocido o usado: la misma pagina y el mismo codigo.
func TestEnlaceInvalidoSiempreLaMismaRespuesta(t *testing.T) {
	s := newLinkServer(t)
	valid := s.path(domain.LinkDiscard, time.Now().Add(30*time.Minute))
	status, done := do(t, http.MethodPost, s.srv.URL+valid)
	if status != http.StatusOK || !strings.Contains(done, "Mensaje descartado") || len(s.q.Items) != 0 {
		t.Fatalf("descarte: %d %s", status, done)
	}
	_, reference := do(t, http.MethodGet, s.srv.URL+"/api/v1/public/mail-security/quarantine/discard?t=x")
	fresh := s.path(domain.LinkRelease, time.Now().Add(30*time.Minute))
	for name, target := range map[string]string{
		"usado (GET)":     valid,
		"firma alterada":  strings.Replace(valid, "sig=", "sig=0", 1),
		"caducado":        s.path(domain.LinkDiscard, time.Now().Add(-time.Minute)),
		"accion cruzada":  strings.Replace(valid, "/discard?", "/release?", 1),
		"sin parametros":  "/api/v1/public/mail-security/quarantine/release",
		"empresa ajena":   strings.Replace(valid, "t="+s.item.TenantID.String(), "t="+uuid.NewString(), 1),
		"caducidad falsa": strings.Replace(valid, "e=", "e=9", 1),
		// Lo que el gateway manda a esta celda sin ser suyo: otra celda, una desconocida o la
		// ruta sin celda con una firma nueva. Vigente y sin usar, y aun asi la misma pagina.
		"otra celda":                   strings.Replace(fresh, "/pe-01/", "/pe-02/", 1),
		"celda desconocida":            strings.Replace(fresh, "/pe-01/", "/zz-99/", 1),
		"firma nueva en ruta de antes": strings.Replace(fresh, "/pe-01/release?", "/release?", 1),
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			status, body := do(t, method, s.srv.URL+target)
			if status != http.StatusForbidden || body != reference {
				t.Errorf("%s %s: %d, la respuesta debe ser identica a la de cualquier enlace invalido", method, name, status)
			}
		}
	}
	if len(s.q.Items) != 0 {
		t.Fatal("el descarte ya se hizo")
	}
}

// Un enlace sin celda de los ya enviados sigue mostrando la pagina y liberando hasta caducar.
func TestEnlaceSinCeldaDeAntesPorHTTP(t *testing.T) {
	s := newLinkServer(t)
	legacy := s.legacyPath(domain.LinkRelease, time.Now().Add(30*time.Minute))
	status, body := do(t, http.MethodGet, s.srv.URL+legacy)
	if status != http.StatusOK || !strings.Contains(body, "Liberar mensaje") || !strings.Contains(body, strings.ReplaceAll(legacy, "&", "&amp;")) {
		t.Fatalf("pagina de un enlace de antes: %d %s", status, body)
	}
	if status, body := do(t, http.MethodPost, s.srv.URL+legacy); status != http.StatusOK || !strings.Contains(body, "Mensaje liberado") || len(s.q.Items) != 0 {
		t.Fatalf("liberar con un enlace de antes: %d %s", status, body)
	}
	if status, _ := do(t, http.MethodPost, s.srv.URL+legacy); status != http.StatusForbidden {
		t.Fatalf("un solo uso tambien para los de antes: %d", status)
	}
}
