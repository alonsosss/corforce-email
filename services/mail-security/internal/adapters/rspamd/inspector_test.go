package rspamd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

type lectura struct {
	method, path, rawQuery, password, body string
}

// controllerDeLectura responde a /stat y /history y anota cada peticion tal cual llego.
func controllerDeLectura(t *testing.T, stat, history string, status int) (*Client, *[]lectura) {
	t.Helper()
	var seen []lectura
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, lectura{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, password: r.Header.Get("Password")})
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		switch r.URL.Path {
		case "/stat":
			_, _ = w.Write([]byte(stat))
		case "/history":
			_, _ = w.Write([]byte(history))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return New(ts.URL, "secreto"), &seen
}

const statJSON = `{"version":"3.11.1","config_id":"abc","uptime":3600,"read_only":false,"scanned":120,"learned":7,
 "actions":{"reject":3,"add header":10,"no action":107,"greylist":0,"soft reject":0,"rewrite subject":0},
 "scan_times":[0.12,0.3,0.06],"spam_count":13,"ham_count":107,"connections":40,"control_connections":9,
 "pools_allocated":1,"bytes_allocated":100,"total_learns":7,
 "fuzzy_hashes":{"rspamd.com":123456},
 "statfiles":[{"revision":7,"used":5,"total":9,"size":1024,"symbol":"BAYES_SPAM","type":"redis","languages":0,"users":1},
              {"revision":7,"used":2,"total":9,"size":1024,"symbol":"BAYES_HAM","type":"redis","languages":0,"users":1}]}`

const historyJSON = `{"version":2,"rows":[
 {"id":"AAA1","unix_time":1758456000,"ip":"203.0.113.9","user":"ana@acme.test","sender_smtp":"ana@acme.test",
  "rcpt_smtp":["bea@acme.test","ceo@acme.test"],"subject":"Informe","score":1.5,"required_score":15,"action":"no action",
  "symbols":{"MIME_GOOD":{"name":"MIME_GOOD","score":-0.1,"options":["multipart/alternative"]},"DKIM_ALLOW":{"name":"DKIM_ALLOW","score":-0.2,"options":["acme.test:s=dkim"]}},
  "size":2048,"scan_time":0.0421,"is_skipped":false,"time_real":0.05,"message-id":"<x@acme.test>"},
 {"id":"BBB2","unix_time":1758456300,"ip":"198.51.100.7","sender_smtp":"spam@ejemplo.org","rcpt_smtp":"ana@acme.test, bea@acme.test",
  "subject":"` + "éé" + `","score":22.4,"required_score":15,"action":"reject","symbols":{},"size":9,"scan_time":0.9,"is_skipped":true}]}`

func TestLasEstadisticasSeLeenConLaContrasenaEnLaCabeceraYNuncaEnLaURL(t *testing.T) {
	c, seen := controllerDeLectura(t, statJSON, historyJSON, http.StatusOK)
	got, err := c.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodGet || (*seen)[0].path != "/stat" || (*seen)[0].password != "secreto" || (*seen)[0].rawQuery != "" {
		t.Fatalf("peticion: %+v", *seen)
	}
	if got.Version != "3.11.1" || got.UptimeSeconds != 3600 || got.Scanned != 120 || got.Learned != 7 || got.SpamCount != 13 || got.HamCount != 107 ||
		got.Connections != 40 || got.ControlConnections != 9 || got.TotalLearns != 7 {
		t.Fatalf("contadores: %+v", got)
	}
	if got.Actions["reject"] != 3 || got.Actions["no action"] != 107 || got.FuzzyHashes["rspamd.com"] != 123456 {
		t.Fatalf("acciones y fuzzy: %+v", got)
	}
	if len(got.Statfiles) != 2 || got.Statfiles[0].Symbol != "BAYES_SPAM" || got.Statfiles[0].Used != 5 || got.Statfiles[1].Symbol != "BAYES_HAM" {
		t.Fatalf("statfiles: %+v", got.Statfiles)
	}
	if got.ScanTime.Samples != 3 || got.ScanTime.AverageMs.String() != "160" || got.ScanTime.MaxMs.String() != "300" {
		t.Fatalf("tiempos: %+v", got.ScanTime)
	}
}

func TestElHistorialSeReduceAlSobreYAlVeredictoSinOpcionesNiCuerpo(t *testing.T) {
	c, seen := controllerDeLectura(t, statJSON, historyJSON, http.StatusOK)
	rows, err := c.History(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].path != "/history" || (*seen)[0].password != "secreto" || (*seen)[0].rawQuery != "" {
		t.Fatalf("peticion: %+v", *seen)
	}
	if len(rows) != 2 {
		t.Fatalf("filas: %+v", rows)
	}
	a := rows[0]
	if a.ID != "AAA1" || a.Time.Unix() != 1758456000 || a.IP != "203.0.113.9" || a.User != "ana@acme.test" || a.Sender != "ana@acme.test" ||
		strings.Join(a.Recipients, ",") != "bea@acme.test,ceo@acme.test" || a.Subject != "Informe" || a.Score.String() != "1.5" ||
		a.RequiredScore.String() != "15" || a.Action != "no action" || a.Size != 2048 || a.ScanTimeMs.String() != "42.1" || a.Skipped {
		t.Fatalf("fila: %+v", a)
	}
	if len(a.Symbols) != 2 || a.Symbols[0].Name != "DKIM_ALLOW" || a.Symbols[0].Score.String() != "-0.2" || a.Symbols[1].Name != "MIME_GOOD" {
		t.Fatalf("simbolos ordenados por nombre y sin opciones: %+v", a.Symbols)
	}
	b := rows[1]
	if strings.Join(b.Recipients, ",") != "ana@acme.test,bea@acme.test" || b.User != "" || !b.Skipped || b.Action != "reject" || b.Subject != "éé" {
		t.Fatalf("fila con destinatarios en una cadena: %+v", b)
	}
	if len(b.Symbols) != 0 || b.Symbols == nil {
		t.Fatalf("sin simbolos debe ser una lista vacia: %+v", b.Symbols)
	}
}

func TestUnAsuntoLargoSeRecortaYUnaFilaSinDestinatariosTraeListaVacia(t *testing.T) {
	largo := strings.Repeat("ñ", domain.MaxRspamdSubjectRunes+50)
	c, _ := controllerDeLectura(t, statJSON, `{"version":2,"rows":[{"id":"C","unix_time":1,"subject":"`+largo+`","rcpt_smtp":null}]}`, http.StatusOK)
	rows, err := c.History(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if got := []rune(rows[0].Subject); len(got) != domain.MaxRspamdSubjectRunes {
		t.Fatalf("asunto de %d runas", len(got))
	}
	if rows[0].Recipients == nil || len(rows[0].Recipients) != 0 {
		t.Fatalf("destinatarios: %v", rows[0].Recipients)
	}
}

func TestSinContrasenaNoSeConsultaYConUnaRechazadaEsNotConfigured(t *testing.T) {
	c, seen := controllerDeLectura(t, statJSON, historyJSON, http.StatusUnauthorized)
	if _, err := c.Stats(context.Background()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("401: %v", err)
	}
	if _, err := c.History(context.Background()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("401: %v", err)
	}
	empty := New("http://127.0.0.1:1", "")
	if _, err := empty.Stats(context.Background()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if _, err := empty.History(context.Background()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if len(*seen) != 2 {
		t.Fatalf("solo se intento con contrasena: %+v", *seen)
	}
}

func TestUnControllerCaidoEsInalcanzableYUnaRespuestaRotaOEnormeEsUnaOrdenFallida(t *testing.T) {
	c, _ := controllerDeLectura(t, statJSON, historyJSON, http.StatusInternalServerError)
	if _, err := c.Stats(context.Background()); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("500: %v", err)
	}
	apagado := New("http://127.0.0.1:9", "secreto")
	if _, err := apagado.History(context.Background()); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("sin escucha: %v", err)
	}
	c, _ = controllerDeLectura(t, `{"scanned":`, `{"version":2,"rows":[{"id":`, http.StatusOK)
	if _, err := c.Stats(context.Background()); !errors.Is(err, domain.ErrEngineCommand) {
		t.Fatalf("stat roto: %v", err)
	}
	if _, err := c.History(context.Background()); !errors.Is(err, domain.ErrEngineCommand) {
		t.Fatalf("history roto: %v", err)
	}
	enorme := `{"version":2,"rows":[{"id":"X","subject":"` + strings.Repeat("x", maxHistoryBody) + `"}]}`
	c, _ = controllerDeLectura(t, statJSON, enorme, http.StatusOK)
	if _, err := c.History(context.Background()); !errors.Is(err, domain.ErrEngineCommand) || !strings.Contains(err.Error(), "supera") {
		t.Fatalf("respuesta enorme: %v", err)
	}
}

func TestNingunErrorRepiteLaContrasena(t *testing.T) {
	c, _ := controllerDeLectura(t, statJSON, historyJSON, http.StatusForbidden)
	_, err := c.Stats(context.Background())
	if err == nil || strings.Contains(err.Error(), "secreto") {
		t.Fatalf("%v", err)
	}
	apagado := New("http://127.0.0.1:9", "secreto")
	if _, err := apagado.Stats(context.Background()); err == nil || strings.Contains(err.Error(), "secreto") {
		t.Fatalf("%v", err)
	}
}
