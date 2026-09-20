package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

const tokenBueno = "cf_token_bueno_0123456789abcdefABCD"

func token(t *testing.T, raw string) domain.APIToken {
	t.Helper()
	tok, err := domain.NewAPIToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// servidor es un Cloudflare de prueba con las respuestas de su API v4. Repite el token recibido en
// los mensajes de error, como podria hacer un proxy o un error de validacion, para comprobar que no
// llega a ningun error.
type servidor struct {
	mu       sync.Mutex
	t        *testing.T
	zonas    int
	registro map[string]map[string]interface{}
	peticion []string
	cuerpos  []map[string]interface{}
	// respuesta fija para una ruta: estado y codigos de Cloudflare.
	fallo map[string]struct {
		status int
		codes  []int
	}
}

func nuevoServidor(t *testing.T) (*servidor, *Client) {
	s := &servidor{t: t, zonas: 120, registro: map[string]map[string]interface{}{}, fallo: map[string]struct {
		status int
		codes  []int
	}{}}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, New(ts.URL, 2*time.Second)
}

func (s *servidor) falla(ruta string, status int, codes ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallo[ruta] = struct {
		status int
		codes  []int
	}{status, codes}
}

func (s *servidor) escribir(w http.ResponseWriter, status int, success bool, result interface{}, info map[string]int, codes []int, tok string) {
	errs := make([]map[string]interface{}, 0, len(codes))
	for _, c := range codes {
		errs = append(errs, map[string]interface{}{"code": c, "message": "rechazado para el token " + tok})
	}
	body := map[string]interface{}{"success": success, "errors": errs, "messages": []string{}, "result": result}
	if info != nil {
		body["result_info"] = info
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peticion = append(s.peticion, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	if f, ok := s.fallo[r.Method+" "+path]; ok {
		s.escribir(w, f.status, false, nil, nil, f.codes, tok)
		return
	}
	if tok != tokenBueno {
		s.escribir(w, http.StatusUnauthorized, false, nil, nil, []int{codeInvalidToken}, tok)
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "/user/tokens/verify":
		s.escribir(w, 200, true, map[string]string{"id": "abc", "status": "active"}, nil, nil, tok)
	case r.Method == http.MethodGet && path == "/zones":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		var zonas []map[string]string
		for i := (page - 1) * per; i < page*per && i < s.zonas; i++ {
			zonas = append(zonas, map[string]string{"id": fmt.Sprintf("%032x", i+1), "name": fmt.Sprintf("Zona%d.test", i)})
		}
		total := (s.zonas + per - 1) / per
		s.escribir(w, 200, true, zonas, map[string]int{"page": page, "per_page": per, "total_pages": total}, nil, tok)
	case strings.HasSuffix(path, "/dns_records") && r.Method == http.MethodGet:
		var out []map[string]interface{}
		for _, rec := range s.registro {
			out = append(out, rec)
		}
		// Un registro de otro nombre que la API no deberia devolver.
		out = append(out, map[string]interface{}{"id": "ffff", "type": r.URL.Query().Get("type"), "name": "otro." + r.URL.Query().Get("name"), "content": "x"})
		s.escribir(w, 200, true, out, map[string]int{"page": 1, "total_pages": 1}, nil, tok)
	case strings.HasSuffix(path, "/dns_records") && r.Method == http.MethodPost:
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.cuerpos = append(s.cuerpos, body)
		body["id"] = fmt.Sprintf("%032x", len(s.registro)+1)
		s.registro[body["id"].(string)] = body
		s.escribir(w, 200, true, body, nil, nil, tok)
	case strings.Contains(path, "/dns_records/") && r.Method == http.MethodPut:
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.cuerpos = append(s.cuerpos, body)
		s.escribir(w, 200, true, body, nil, nil, tok)
	case strings.Contains(path, "/dns_records/") && r.Method == http.MethodDelete:
		id := path[strings.LastIndex(path, "/")+1:]
		if _, ok := s.registro[id]; !ok {
			s.escribir(w, 404, false, nil, nil, []int{81044}, tok)
			return
		}
		delete(s.registro, id)
		s.escribir(w, 200, true, map[string]string{"id": id}, nil, nil, tok)
	default:
		s.escribir(w, 404, false, nil, nil, []int{7003}, tok)
	}
}

var zona = domain.DNSZone{ID: "023e105f4ecef8ad9ca31a8372d0c353", Name: "acme.com"}

func sinToken(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), tokenBueno) || strings.Contains(err.Error(), "token_malo")) {
		t.Errorf("el error lleva el token: %v", err)
	}
}

func TestVerificaElTokenYLoEnviaSoloEnLaCabecera(t *testing.T) {
	s, c := nuevoServidor(t)
	if err := c.VerifyToken(context.Background(), token(t, tokenBueno)); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.peticion {
		if strings.Contains(p, tokenBueno) {
			t.Errorf("el token viaja en la URL: %s", p)
		}
	}
	err := c.VerifyToken(context.Background(), token(t, "cf_token_malo_0123456789abcdef"))
	if !errors.Is(err, domain.ErrDNSProviderTokenInvalid) {
		t.Errorf("token invalido: %v", err)
	}
	sinToken(t, err)
}

func TestTokenNoActivo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"x","status":"disabled"}}`))
	}))
	defer ts.Close()
	err := New(ts.URL, time.Second).VerifyToken(context.Background(), token(t, tokenBueno))
	if !errors.Is(err, domain.ErrDNSProviderTokenInvalid) {
		t.Errorf("token deshabilitado: %v", err)
	}
}

func TestListaTodasLasPaginasDeZonas(t *testing.T) {
	s, c := nuevoServidor(t)
	zones, err := c.ListZones(context.Background(), token(t, tokenBueno))
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 120 || zones[0].Name != "zona0.test" || zones[119].Name != "zona119.test" {
		t.Errorf("zonas %d: %v ... %v", len(zones), zones[0], zones[len(zones)-1])
	}
	if n := len(s.peticion); n != 3 {
		t.Errorf("paginas pedidas %d: %v", n, s.peticion)
	}
	s.zonas = 0
	if zones, err := c.ListZones(context.Background(), token(t, tokenBueno)); err != nil || len(zones) != 0 {
		t.Errorf("sin zonas: %v %v", zones, err)
	}
}

func TestTraduceLosErroresDeCloudflare(t *testing.T) {
	ctx := context.Background()
	for nombre, c := range map[string]struct {
		status int
		codes  []int
		want   error
	}{
		"token invalido":                {401, []int{codeInvalidToken}, domain.ErrDNSProviderTokenInvalid},
		"cabecera mal formada":          {400, []int{codeInvalidAuthFormat}, domain.ErrDNSProviderTokenInvalid},
		"sin permiso":                   {403, []int{codeUnauthorized}, domain.ErrDNSProviderPermissionDenied},
		"error de autenticacion":        {400, []int{codeAuthenticationError}, domain.ErrDNSProviderPermissionDenied},
		"zona ajena":                    {404, []int{7003}, domain.ErrDNSZoneNotFound},
		"limite de peticiones":          {429, []int{971}, domain.ErrDNSProviderRateLimited},
		"registro identico ya existe":   {400, []int{codeIdenticalRecordExists}, domain.ErrDNSRecordExists},
		"registro con los mismos datos": {400, []int{codeRecordExistsSameName}, domain.ErrDNSRecordExists},
		"contenido rechazado":           {400, []int{9005}, domain.ErrDNSProviderRejected},
		"CNAME en el mismo nombre":      {400, []int{81053}, domain.ErrDNSProviderRejected},
		"fallo de Cloudflare":           {502, nil, domain.ErrDNSProviderUnavailable},
	} {
		s, cl := nuevoServidor(t)
		s.falla("POST /zones/"+zona.ID+"/dns_records", c.status, c.codes...)
		err := cl.CreateRecord(ctx, token(t, tokenBueno), zona, domain.ProviderRecord{Type: "TXT", Name: "acme.com", Content: "v=spf1 -all"})
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v", nombre, err)
		}
		sinToken(t, err)
		if err != nil && strings.Contains(err.Error(), "rechazado para el token") {
			t.Errorf("%s: el error repite el mensaje de Cloudflare: %v", nombre, err)
		}
	}
}

func TestEscribeRegistrosConLaMarcaYElTTLAutomatico(t *testing.T) {
	s, c := nuevoServidor(t)
	ctx := context.Background()
	tok := token(t, tokenBueno)
	if err := c.CreateRecord(ctx, tok, zona, domain.ProviderRecord{Type: "MX", Name: "acme.com", Content: "mx.plataforma.example", Priority: 10, Comment: domain.ManagedRecordComment}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateRecord(ctx, tok, zona, domain.ProviderRecord{Type: "TXT", Name: "acme.com", Content: "v=spf1 -all", Comment: domain.ManagedRecordComment}); err != nil {
		t.Fatal(err)
	}
	mx, txt := s.cuerpos[0], s.cuerpos[1]
	if mx["priority"] != float64(10) || mx["ttl"] != float64(1) || mx["comment"] != domain.ManagedRecordComment || mx["content"] != "mx.plataforma.example" {
		t.Errorf("MX %v", mx)
	}
	if _, ok := txt["priority"]; ok {
		t.Errorf("un TXT no lleva prioridad: %v", txt)
	}
	// Cloudflare pide el contenido de un TXT entre comillas: sin ellas lo acepta pero marca el
	// registro con un aviso en su panel. El MX no las lleva.
	if txt["content"] != `"v=spf1 -all"` {
		t.Errorf("el TXT viaja entre comillas: %v", txt["content"])
	}

	records, err := c.ListRecords(ctx, tok, zona, "TXT", "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Content != `"v=spf1 -all"` || !records[0].Managed() {
		t.Errorf("solo el TXT de ese nombre y tipo: %+v", records)
	}
	if !strings.Contains(s.peticion[len(s.peticion)-1], "name=acme.com") || !strings.Contains(s.peticion[len(s.peticion)-1], "type=TXT") {
		t.Errorf("filtro %s", s.peticion[len(s.peticion)-1])
	}

	if err := c.UpdateRecord(ctx, tok, zona, domain.ProviderRecord{ID: records[0].ID, Type: "TXT", Name: "acme.com", Content: "v=spf1 mx -all"}); err != nil {
		t.Fatal(err)
	}
	if got := s.cuerpos[len(s.cuerpos)-1]["content"]; got != `"v=spf1 mx -all"` {
		t.Errorf("actualizar tambien escribe entre comillas: %v", got)
	}
	larga := strings.Repeat("k", 300)
	if err := c.CreateRecord(ctx, tok, zona, domain.ProviderRecord{Type: "TXT", Name: "k._domainkey.acme.com", Content: larga}); err != nil {
		t.Fatal(err)
	}
	if got, want := s.cuerpos[len(s.cuerpos)-1]["content"], `"`+strings.Repeat("k", 255)+`" "`+strings.Repeat("k", 45)+`"`; got != want {
		t.Errorf("una clave de 300 caracteres va en dos cadenas de <= 255: %v", got)
	}
	if err := c.DeleteRecord(ctx, tok, zona, records[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRecord(ctx, tok, zona, records[0].ID); err != nil {
		t.Errorf("retirar dos veces es idempotente: %v", err)
	}
}

func TestRechazaIdsQueNoSonDeCloudflare(t *testing.T) {
	s, c := nuevoServidor(t)
	ctx := context.Background()
	tok := token(t, tokenBueno)
	malo := domain.DNSZone{ID: "../../user/tokens", Name: "acme.com"}
	if _, err := c.ListRecords(ctx, tok, malo, "TXT", "acme.com"); !errors.Is(err, domain.ErrDNSZoneNotFound) {
		t.Errorf("zona: %v", err)
	}
	if err := c.DeleteRecord(ctx, tok, zona, "abc/../x"); !errors.Is(err, domain.ErrDNSProviderRejected) {
		t.Errorf("registro: %v", err)
	}
	if len(s.peticion) != 0 {
		t.Errorf("salio una peticion: %v", s.peticion)
	}
}

// Una redireccion llevaria el token a otro host: no se sigue.
func TestNoSigueRedirecciones(t *testing.T) {
	var ajenas atomic.Int32
	ajeno := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ajenas.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
	}))
	defer ajeno.Close()
	redirige := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ajeno.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirige.Close()
	err := New(redirige.URL, time.Second).VerifyToken(context.Background(), token(t, tokenBueno))
	if !errors.Is(err, domain.ErrDNSProviderUnavailable) {
		t.Errorf("redireccion: %v", err)
	}
	if ajenas.Load() != 0 {
		t.Error("la peticion llego al otro host")
	}
}

func TestRespuestasAnomalas(t *testing.T) {
	ctx := context.Background()
	for nombre, handler := range map[string]http.HandlerFunc{
		"no es JSON": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"demasiado grande": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"success":true,"result":"` + strings.Repeat("a", maxResponseBytes) + `"}`))
		},
		"lenta": func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) },
	} {
		ts := httptest.NewServer(handler)
		err := New(ts.URL, 100*time.Millisecond).VerifyToken(ctx, token(t, tokenBueno))
		if !errors.Is(err, domain.ErrDNSProviderUnavailable) {
			t.Errorf("%s: %v", nombre, err)
		}
		ts.Close()
	}
	cancelado, cancel := context.WithCancel(ctx)
	cancel()
	if err := New("http://127.0.0.1:1", time.Second).VerifyToken(cancelado, token(t, tokenBueno)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelada: %v", err)
	}
	if err := New("http://127.0.0.1:1", time.Second).VerifyToken(ctx, domain.APIToken{}); !errors.Is(err, domain.ErrDNSProviderTokenInvalid) {
		t.Errorf("sin token: %v", err)
	}
}

func TestPaginacionAnomalaSeCorta(t *testing.T) {
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"ab12","name":"z.test"}],"result_info":{"page":1,"total_pages":100000}}`))
	}))
	defer ts.Close()
	if _, err := New(ts.URL, time.Second).ListZones(context.Background(), token(t, tokenBueno)); !errors.Is(err, domain.ErrDNSProviderRejected) {
		t.Errorf("paginas sin fin: %v", err)
	}
	if n.Load() != maxPages {
		t.Errorf("peticiones %d", n.Load())
	}
}
