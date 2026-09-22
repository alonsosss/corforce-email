package cli

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
)

const informe = "Core Force Mail: anclas de las cadenas de auditoria\nFecha: 2026-09-22T00:00:00Z\n\n-----BEGIN CORE FORCE MAIL AUDIT ANCHORS-----\nformat: 1\n-----END CORE FORCE MAIL AUDIT ANCHORS-----\nsignature: none\n"

func TestElTextoPegadoSeLeeTalCual(t *testing.T) {
	if got := extractText([]byte(informe)); got != informe {
		t.Fatalf("%q", got)
	}
	// Un texto cuya primera linea parece una cabecera ("Fecha: ...") sigue sin ser un correo:
	// no lleva Content-Type, From, Message-Id ni Received.
	pegado := strings.TrimPrefix(informe, "Core Force Mail: anclas de las cadenas de auditoria\n")
	if got := extractText([]byte(pegado)); got != pegado {
		t.Fatalf("%q", got)
	}
}

func TestUnCorreoDeUnaSolaParteEnTextoPlano(t *testing.T) {
	eml := "From: no-reply@example.org\r\nSubject: anclas\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + strings.ReplaceAll(informe, "\n", "\r\n")
	got := extractText([]byte(eml))
	if strings.ReplaceAll(got, "\r\n", "\n") != informe {
		t.Fatalf("%q", got)
	}
	// Sin Content-Type se asume texto plano, que es lo que envia transactional.
	eml = "From: no-reply@example.org\r\nSubject: anclas\r\n\r\n" + informe
	if got := extractText([]byte(eml)); got != informe {
		t.Fatalf("%q", got)
	}
}

func TestUnaParteEnBase64SeDecodifica(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(informe))
	var folded strings.Builder
	for len(encoded) > 76 {
		folded.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	folded.WriteString(encoded + "\r\n")
	eml := "Message-Id: <x@example.org>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: multipart/alternative; boundary=\"b2\"\r\n\r\n" +
		"--b2\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n" +
		"--b2\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + folded.String() +
		"--b2--\r\n--b1--\r\n"
	if got := extractText([]byte(eml)); got != informe {
		t.Fatalf("%q", got)
	}
}

func TestUnCorreoSinParteDeTextoSeDevuelveEnteroYNoSeLee(t *testing.T) {
	eml := "Received: from x\r\nContent-Type: multipart/alternative; boundary=\"b\"\r\n\r\n--b\r\nContent-Type: text/html\r\n\r\n<p>solo html</p>\r\n--b--\r\n"
	if got := extractText([]byte(eml)); got != eml {
		t.Fatalf("%q", got)
	}
	eml = "From: a@example.org\r\nContent-Type: application/pdf\r\n\r\n%PDF"
	if got := extractText([]byte(eml)); got != eml {
		t.Fatalf("%q", got)
	}
}

func intactFacts() map[domain.ChainName]domain.ChainFacts {
	return map[domain.ChainName]domain.ChainFacts{
		domain.ChainAuditLogs:      {HeadSeq: 55, HashAtSeq: hashLogs, AnchorRecorded: true},
		domain.ChainSecurityEvents: {HeadSeq: 3, HashAtSeq: hashEvents, AnchorRecorded: true},
	}
}

func TestLasOpcionesPuedenIrAntesODespuesDelFichero(t *testing.T) {
	path := write(t, signed(t, report(acme()), ring(t, keyA, "")))
	for _, args := range [][]string{
		{path, "--dsn", "d", "--tenant", "acme"},
		{"--dsn", "d", path, "--tenant", "acme"},
		{"--dsn=d", "--tenant=acme", path},
	} {
		s := &scriptedFacts{facts: intactFacts()}
		if code, out, errOut := run(t, args, opener(s)); code != ExitOK || s.dsn != "d" {
			t.Fatalf("%v: %d %q %q", args, code, out, errOut)
		}
	}
	if code, _, _ := run(t, []string{path, "--desconocida"}, nil); code != ExitError {
		t.Fatal("una opcion desconocida es un error de uso")
	}
	if code, _, _ := run(t, []string{path, path}, nil); code != ExitError {
		t.Fatal("dos ficheros es un error de uso")
	}
}
