package rawmail

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

const plain = "From: \"Ventas\" <Ventas@Empresa.test>\r\nTo: ana@destino.test\r\nMessage-ID: <abc.1@empresa.test>\r\nSubject: =?utf-8?q?Factura_n=C2=BA_7?=\r\n\r\nHola Ana\r\n"

func TestParseMensajeSimple(t *testing.T) {
	m, err := Parse([]byte(plain), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if m.From.Email != "Ventas@Empresa.test" || m.From.Name != "Ventas" {
		t.Errorf("remitente: %+v", m.From)
	}
	if m.MessageID != "abc.1@empresa.test" {
		t.Errorf("message-id: %q", m.MessageID)
	}
	if m.Subject != "Factura nº 7" || !strings.Contains(m.Text, "Hola Ana") || m.HasAttachments || m.Parts != 1 {
		t.Errorf("mensaje: %+v", m)
	}
}

func multipart(parts ...string) string {
	var b strings.Builder
	b.WriteString("From: a@empresa.test\r\nTo: b@destino.test\r\nSubject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"LIM\"\r\n\r\n")
	for _, p := range parts {
		b.WriteString("--LIM\r\n" + p + "\r\n")
	}
	b.WriteString("--LIM--\r\n")
	return b.String()
}

func TestParseDetectaAdjuntos(t *testing.T) {
	raw := multipart(
		"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Hola</p>",
		"Content-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"f.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK",
	)
	m, err := Parse([]byte(raw), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasAttachments || m.HTML != "<p>Hola</p>" || m.Parts != 3 {
		t.Fatalf("mensaje: %+v", m)
	}
	// Un texto marcado como adjunto tambien se analiza.
	m, err = Parse([]byte(multipart("Content-Type: text/plain\r\nContent-Disposition: attachment\r\n\r\nlista")), Limits{})
	if err != nil || !m.HasAttachments || m.Text != "" {
		t.Fatalf("texto adjunto: %+v %v", m, err)
	}
}

func TestParseRechazaLoMalformado(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want error
	}{
		"sin From":        {"To: a@b.test\r\nSubject: x\r\n\r\nhola", ErrFrom},
		"dos From":        {"From: a@b.test, c@d.test\r\n\r\nhola", ErrFrom},
		"From ilegible":   {"From: <<no>>\r\n\r\nhola", ErrFrom},
		"linea larga":     {"From: a@b.test\r\n\r\n" + strings.Repeat("x", 1200), ErrLineTooLong},
		"base64 roto":     {multipart("Content-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!"), ErrMalformed},
		"Sender ilegible": {"From: a@b.test\r\nSender: <<>>\r\n\r\nhola", ErrMalformed},
	}
	for name, tc := range cases {
		if _, err := Parse([]byte(tc.raw), Limits{}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, se esperaba %v", name, err, tc.want)
		}
	}
}

func TestParseLimites(t *testing.T) {
	if _, err := Parse([]byte(plain), Limits{MaxBytes: 10}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("tamano: %v", err)
	}
	parts := make([]string, 5)
	for i := range parts {
		parts[i] = "Content-Type: text/plain\r\n\r\nx"
	}
	if _, err := Parse([]byte(multipart(parts...)), Limits{MaxParts: 3}); !errors.Is(err, ErrTooManyParts) {
		t.Errorf("partes: %v", err)
	}
	nested := "Content-Type: text/plain\r\n\r\nfondo"
	for i := 0; i < 4; i++ {
		b := "B" + string(rune('a'+i))
		nested = "Content-Type: multipart/mixed; boundary=\"" + b + "\"\r\n\r\n--" + b + "\r\n" + nested + "\r\n--" + b + "--"
	}
	if _, err := Parse([]byte("From: a@b.test\r\n"+nested+"\r\n"), Limits{MaxDepth: 2}); !errors.Is(err, ErrMalformed) {
		t.Errorf("anidamiento: %v", err)
	}
	if _, err := Parse([]byte("From: a@b.test\r\n"+nested+"\r\n"), Limits{}); err != nil {
		t.Errorf("anidamiento dentro del limite: %v", err)
	}
}

func TestSanitizeQuitaCabecerasPeligrosas(t *testing.T) {
	raw := "From: a@empresa.test\nBcc: oculto@x.test\nX-SES-CONFIGURATION-SET: marketing\nx-ses-message-tags: tenant_id=otra\nReturn-Path: <x@y.test>\nSubject: hola\n  plegado\nTo: b@destino.test\n\ncuerpo\nBcc: esto es cuerpo\n"
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	out, err := Sanitize([]byte(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, gone := range []string{"oculto@x.test", "X-SES", "x-ses", "Return-Path"} {
		if strings.Contains(s, gone) {
			t.Errorf("sigue %q en %q", gone, s)
		}
	}
	for _, kept := range []string{"Subject: hola\r\n  plegado\r\n", "Date: Wed, 23 Sep 2026 10:00:00 +0000\r\n", "MIME-Version: 1.0\r\n", "\r\n\r\ncuerpo\r\nBcc: esto es cuerpo\r\n"} {
		if !strings.Contains(s, kept) {
			t.Errorf("falta %q en %q", kept, s)
		}
	}
	if bytes.Contains(out, []byte("\r\r")) || strings.Count(s, "\n") != strings.Count(s, "\r\n") {
		t.Errorf("finales de linea sin normalizar: %q", s)
	}
	// Una cabecera plegada que se retira se lleva sus continuaciones.
	out, _ = Sanitize([]byte("From: a@b.test\r\nBcc: uno@x.test,\r\n dos@x.test\r\nDate: x\r\nMIME-Version: 1.0\r\n\r\nc"), now)
	if strings.Contains(string(out), "dos@x.test") || strings.Count(string(out), "Date:") != 1 {
		t.Errorf("plegado: %q", out)
	}
	if _, err := Sanitize([]byte("From: a@b.test"), now); !errors.Is(err, ErrMalformed) {
		t.Errorf("sin cuerpo: %v", err)
	}
	if _, err := Sanitize([]byte("From: a@b.test\r\nsin dos puntos\r\n\r\nc"), now); !errors.Is(err, ErrMalformed) {
		t.Errorf("cabecera sin nombre: %v", err)
	}
}
