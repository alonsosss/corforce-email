package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const mimeDePrueba = "From: Tienda <ventas@acme.test>\r\nTo: cliente@ejemplo.org\r\nSubject: Oferta secreta de otono\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Hola Maria, tu codigo es DESCUENTO-4411</p>\r\n"

func newSpamCheckUC(scanner *apptest.SpamScanner) (*SpamCheckUseCase, *apptest.SpamCheckMetrics, *observer.ObservedLogs) {
	metrics := &apptest.SpamCheckMetrics{}
	core, logs := observer.New(zap.DebugLevel)
	return NewSpamCheckUseCase(scanner, metrics, zap.New(core)), metrics, logs
}

func TestLaPuntuacionOrdenaLosSimbolosDelMasPesadoAlMasLigero(t *testing.T) {
	scanner := &apptest.SpamScanner{Result: domain.SpamCheckResult{
		Score: decimal.RequireFromString("3.2"), Required: decimal.NewFromInt(15), Action: "no action",
		Symbols: []domain.SpamCheckSymbol{
			{Name: "MIME_HTML_ONLY", Score: decimal.RequireFromString("0.2")},
			{Name: "DMARC_NA", Score: decimal.Zero},
			{Name: "HTML_SHORT_LINK_IMG_1", Score: decimal.RequireFromString("2")},
			{Name: "ARC_NA", Score: decimal.Zero},
			{Name: "MIME_GOOD", Score: decimal.RequireFromString("-0.1")},
		},
	}}
	uc, metrics, _ := newSpamCheckUC(scanner)
	out, err := uc.Check(context.Background(), []byte(mimeDePrueba))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range out.Symbols {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "HTML_SHORT_LINK_IMG_1,MIME_HTML_ONLY,ARC_NA,DMARC_NA,MIME_GOOD" {
		t.Fatalf("orden: %s", got)
	}
	if len(scanner.Messages) != 1 || !bytes.Equal(scanner.Messages[0], []byte(mimeDePrueba)) {
		t.Fatalf("mensaje enviado a rspamd: %q", scanner.Messages)
	}
	if metrics.Count(domain.SpamCheckScanned) != 1 {
		t.Fatalf("metrica scanned: %d", metrics.Count(domain.SpamCheckScanned))
	}
}

func TestUnMensajeVacioOMayorQueElTopeNoLlegaARspamd(t *testing.T) {
	scanner := &apptest.SpamScanner{}
	uc, metrics, _ := newSpamCheckUC(scanner)
	for _, msg := range [][]byte{nil, []byte(""), []byte(" \r\n\t ")} {
		if _, err := uc.Check(context.Background(), msg); !errors.Is(err, domain.ErrSpamCheckEmpty) {
			t.Fatalf("vacio %q: %v", msg, err)
		}
	}
	grande := bytes.Repeat([]byte("a"), domain.MaxSpamCheckMessageBytes+1)
	if _, err := uc.Check(context.Background(), grande); !errors.Is(err, domain.ErrSpamCheckTooLarge) {
		t.Fatalf("mayor que el tope: %v", err)
	}
	if len(scanner.Messages) != 0 || metrics.Count(domain.SpamCheckInvalid) != 4 {
		t.Fatalf("llamadas %d, invalid %d", len(scanner.Messages), metrics.Count(domain.SpamCheckInvalid))
	}
	justo := bytes.Repeat([]byte("a"), domain.MaxSpamCheckMessageBytes)
	if _, err := uc.Check(context.Background(), justo); err != nil {
		t.Fatalf("en el tope: %v", err)
	}
}

func TestRspamdCaidoOSinContrasenaSeCuentanPorSeparado(t *testing.T) {
	scanner := &apptest.SpamScanner{Err: fmt.Errorf("%w: conexion rechazada", domain.ErrEngineUnreachable)}
	uc, metrics, _ := newSpamCheckUC(scanner)
	if _, err := uc.Check(context.Background(), []byte(mimeDePrueba)); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("caido: %v", err)
	}
	scanner.Err = domain.ErrNotConfigured
	if _, err := uc.Check(context.Background(), []byte(mimeDePrueba)); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if metrics.Count(domain.SpamCheckUnavailable) != 1 || metrics.Count(domain.SpamCheckNotConfigured) != 1 {
		t.Fatalf("metricas: unavailable %d, not_configured %d", metrics.Count(domain.SpamCheckUnavailable), metrics.Count(domain.SpamCheckNotConfigured))
	}

	sinScanner := NewSpamCheckUseCase(nil, nil, zap.NewNop())
	if _, err := sinScanner.Check(context.Background(), []byte(mimeDePrueba)); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("sin scanner: %v", err)
	}
}

// El registro lleva tamano y veredicto, nunca remitente, destinatario, asunto ni cuerpo.
func TestElRegistroNoLlevaNadaDelMensaje(t *testing.T) {
	scanner := &apptest.SpamScanner{Result: domain.SpamCheckResult{Score: decimal.NewFromInt(1), Required: decimal.NewFromInt(15), Action: "no action"}}
	uc, _, logs := newSpamCheckUC(scanner)
	if _, err := uc.Check(context.Background(), []byte(mimeDePrueba)); err != nil {
		t.Fatal(err)
	}
	scanner.Err = domain.ErrEngineUnreachable
	_, _ = uc.Check(context.Background(), []byte(mimeDePrueba))
	if logs.Len() != 2 {
		t.Fatalf("lineas de registro: %d", logs.Len())
	}
	for _, entry := range logs.All() {
		enc := zapcore.NewMapObjectEncoder()
		for _, f := range entry.Context {
			f.AddTo(enc)
		}
		linea := entry.Message + fmt.Sprint(enc.Fields)
		for _, dato := range []string{"acme.test", "ejemplo.org", "Oferta", "Maria", "DESCUENTO"} {
			if strings.Contains(linea, dato) {
				t.Fatalf("el registro lleva %q: %s", dato, linea)
			}
		}
	}
}
