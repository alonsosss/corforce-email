package http

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type exporterServer struct {
	srv        *httptest.Server
	quarantine *apptest.Quarantine
}

func newExporterServer(t *testing.T) *exporterServer {
	t.Helper()
	dir, policy, store := apptest.NewDirectory(), apptest.NewPolicyReader(), apptest.NewStore()
	dir.Mailboxes["bea@acme.test"] = domain.Mailbox{TenantID: uuid.New(), Username: "bea@acme.test", Domain: "acme.test", Active: 1}
	q := &apptest.Quarantine{}
	logger := zap.NewNop()
	uc := app.NewEngineUseCase(app.EngineDeps{
		Directory: dir, Policy: policy, Quarantine: q,
		Tx: &apptest.Tx{}, Documents: apptest.NewDocuments(),
		Sync: app.NewRedisSync(store, dir, policy, logger), Store: store, Events: &apptest.Publisher{}, Logger: logger, LogLines: 10,
	})
	srv := httptest.NewServer(NewExporterHandler(uc, 1<<20, logger).Routes(""))
	t.Cleanup(srv.Close)
	return &exporterServer{srv: srv, quarantine: q}
}

// rspamdPipeBody arma el cuerpo como lo manda metadata_exporter de Rspamd 4.1.4 (formatter
// multipart, lua_util.table_to_multipart_body): frontera entre comillas en la cabecera, cada
// parte con Content-Transfer-Encoding binary y el mensaje antes que los metadatos.
func rspamdPipeBody(metadata string) (string, []byte) {
	const boundary = "eb8db23f15e14e06"
	var b bytes.Buffer
	fmt.Fprintf(&b, "--%s\r\nContent-Disposition: form-data; name=\"message\"; filename=\"message.eml\"\r\n", boundary)
	b.WriteString("Content-Type: message/rfc822\r\nContent-Transfer-Encoding: binary\r\n\r\n")
	b.WriteString("From: <ana@acme.test>\r\nTo: <bea@acme.test>\r\nSubject: eicar\r\n\r\ncuerpo\r\n\r\n")
	fmt.Fprintf(&b, "--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\n", boundary)
	b.WriteString("Content-Type: application/json\r\nContent-Transfer-Encoding: binary\r\n\r\n")
	b.WriteString(metadata)
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return fmt.Sprintf("multipart/form-data; boundary=\"%s\"", boundary), b.Bytes()
}

func postPipe(t *testing.T, url, metadata string) int {
	t.Helper()
	contentType, body := rspamdPipeBody(metadata)
	resp, err := http.Post(url+"/pipe", contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPipeAceptaLosMetadatosDeRspamd4(t *testing.T) {
	s := newExporterServer(t)
	// Simbolos como objetos, que es lo que manda get_symbols_all() en Rspamd 4 (CLAM_VIRUS no
	// aparece: lo consume el compuesto VIRUS_FOUND).
	metadata := `{"rcpt":["bea@acme.test"],"subject":"eicar","ip":"172.30.29.9","message_id":"x7@e2e.test",` +
		`"qid":"4Q1","rspamd_server":"rspamd","size":436,"user":"ana@acme.test","scan_time":222,"fuzzy":[],` +
		`"header_to":["<bea@acme.test>"],"from":"ana@acme.test","action":"reject","score":2028.500000,` +
		`"symbols":[{"groups":["mime_types"],"name":"MIME_BAD_EXTENSION","weight":1,"score":10.100000,"options":["com"]},` +
		`{"groups":["composite"],"name":"VIRUS_FOUND","weight":1,"score":2000.000000}]}`
	if code := postPipe(t, s.srv.URL, metadata); code != http.StatusOK {
		t.Fatalf("metadatos de Rspamd 4: %d", code)
	}
	if len(s.quarantine.Items) != 1 {
		t.Fatalf("debia guardar una fila para bea: %d", len(s.quarantine.Items))
	}
	it := s.quarantine.Items[0]
	if it.Rcpt != "bea@acme.test" || it.Action != "reject" || !it.Score.Equal(decimal.RequireFromString("2028.5")) {
		t.Fatalf("fila: %+v", it)
	}
	if !slices.Equal(it.Symbols, []string{"MIME_BAD_EXTENSION", "VIRUS_FOUND"}) {
		t.Fatalf("se guardan los nombres de los simbolos: %v", it.Symbols)
	}
}

func TestPipeAdmiteSimbolosComoCadenasYRcptDesconocido(t *testing.T) {
	s := newExporterServer(t)
	if code := postPipe(t, s.srv.URL, `{"rcpt":"bea@acme.test","qid":"Q2","action":"reject","score":20,"symbols":["BAD_SUBJECT"]}`); code != http.StatusOK {
		t.Fatalf("simbolos como cadenas: %d", code)
	}
	if len(s.quarantine.Items) != 1 || !slices.Equal(s.quarantine.Items[0].Symbols, []string{"BAD_SUBJECT"}) {
		t.Fatalf("fila: %+v", s.quarantine.Items)
	}
	// Sin destinatarios SMTP Rspamd manda rcpt "unknown": no hay buzon al que guardarlo.
	if code := postPipe(t, s.srv.URL, `{"rcpt":"unknown","qid":"Q3","action":"reject","score":20,"symbols":[]}`); code != http.StatusOK {
		t.Fatalf("rcpt unknown: %d", code)
	}
	if len(s.quarantine.Items) != 1 {
		t.Fatalf("rcpt unknown no guarda nada: %d filas", len(s.quarantine.Items))
	}
	if code := postPipe(t, s.srv.URL, `{"rcpt":["bea@acme.test"],"symbols":[{"score":1}],"score":"no"}`); code != http.StatusBadRequest {
		t.Fatalf("metadatos invalidos: %d", code)
	}
}
