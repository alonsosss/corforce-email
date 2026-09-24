//go:build integration

package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Conversaciones, bandeja inteligente, escudo y baja por correo con los adaptadores reales contra el
// IMAP en memoria (sin THREAD: agrupa el servicio) y el submission en proceso.
func TestIntegracionConversacionesFichaYBaja(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	mb, err := env.store.Open(ctx, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	put := func(folder, raw string) {
		t.Helper()
		if _, err := mb.Append(ctx, folder, []byte(strings.ReplaceAll(raw, "\n", "\r\n")), nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	put("INBOX", "From: Cliente <c@cliente.test>\nTo: ana@empresa.test\nSubject: Presupuesto\nDate: Mon, 1 Sep 2026 10:00:00 +0000\nMessage-ID: <p1@cliente.test>\n\nHola\n")
	put("Sent", "From: Ana <ana@empresa.test>\nTo: c@cliente.test\nSubject: Re: Presupuesto\nDate: Mon, 1 Sep 2026 11:00:00 +0000\nMessage-ID: <r1@empresa.test>\nIn-Reply-To: <p1@cliente.test>\n\nAdjunto\n")
	put("INBOX", "From: Cliente <c@cliente.test>\nTo: ana@empresa.test\nSubject: Re: Presupuesto\nDate: Mon, 1 Sep 2026 12:00:00 +0000\nMessage-ID: <p2@cliente.test>\nReferences: <p1@cliente.test> <r1@empresa.test>\n\nAceptado\n")
	put("INBOX", "From: \"Tienda\" <news@tienda.test>\nTo: ana@empresa.test\nSubject: Ofertas\nDate: Mon, 1 Sep 2026 13:00:00 +0000\nList-Unsubscribe: <mailto:baja@tienda.test?subject=baja%20ana>\n\nOfertas\n")
	put("INBOX", "From: \"Ana Gerente\" <ana@ernpresa.test>\nTo: ana@empresa.test\nSubject: Transferencia urgente\nDate: Mon, 1 Sep 2026 14:00:00 +0000\nAuthentication-Results: mx.empresa.test; spf=fail smtp.mailfrom=ernpresa.test; dmarc=fail\n\nPaga hoy\n")
	_ = mb.Close()

	token, _, err := env.svc.Login(ctx, mailbox, password, "203.0.113.7", "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := env.svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	q, _ := domain.NewListQuery(1, 10, "")
	threads, err := env.svc.ListThreads(ctx, sess, "INBOX", q)
	if err != nil {
		t.Fatal(err)
	}
	if threads.Total != 3 {
		t.Fatalf("tres conversaciones en INBOX: %+v", threads)
	}
	var negotiation domain.ThreadSummary
	for _, th := range threads.Items {
		if th.Size == 2 {
			negotiation = th
		}
	}
	if negotiation.Latest.Subject != "Re: Presupuesto" || negotiation.Unread != 2 {
		t.Fatalf("la negociacion se agrupa: %+v", threads.Items)
	}
	conv, err := env.svc.Conversation(ctx, sess, "INBOX", negotiation.Latest.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 3 || conv[0].MessageID != "p1@cliente.test" || conv[1].Folder != "Sent" || conv[2].MessageID != "p2@cliente.test" {
		t.Fatalf("la conversacion suma la respuesta de Enviados en orden: %+v", conv)
	}

	news, _ := q.WithFilter(domain.SearchFilter{Category: domain.CategoryNewsletters})
	page, err := env.svc.ListMessages(ctx, sess, "INBOX", news)
	if err != nil || page.Total != 1 || page.Items[0].Subject != "Ofertas" {
		t.Fatalf("pestana de boletines: %+v %v", page, err)
	}
	newsUID := page.Items[0].UID

	all, _ := env.svc.ListMessages(ctx, sess, "INBOX", q)
	var fraudUID uint32
	for _, e := range all.Items {
		if e.Subject == "Transferencia urgente" {
			fraudUID = e.UID
		}
	}
	insight, err := env.svc.SenderInsight(ctx, sess, "INBOX", fraudUID)
	if err != nil {
		t.Fatal(err)
	}
	if insight.Shield.Level != domain.ShieldDanger || !insight.Shield.External {
		t.Fatalf("escudo: %+v", insight.Shield)
	}

	res, err := env.svc.Unsubscribe(ctx, sess, "INBOX", newsUID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != domain.UnsubscribeMailto || res.Target != "baja@tienda.test" {
		t.Fatalf("baja: %+v", res)
	}
	got := env.smtp.received()
	last := got[len(got)-1]
	if last.from != mailbox || len(last.rcpts) != 1 || last.rcpts[0] != "baja@tienda.test" || !strings.Contains(string(last.data), "Subject: baja ana") {
		t.Fatalf("correo de baja: from=%s rcpts=%v", last.from, last.rcpts)
	}
}

// La respuesta recibida a un correo propio: el propio solo esta en Enviados y la respuesta lo cita en
// In-Reply-To, sin References. Abrirla desde INBOX trae los dos.
func TestIntegracionConversacionDesdeLaRespuestaTraeElPropioDeEnviados(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	mb, err := env.store.Open(ctx, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	put := func(folder, raw string) {
		t.Helper()
		if _, err := mb.Append(ctx, folder, []byte(strings.ReplaceAll(raw, "\n", "\r\n")), nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	put("Sent", "From: Ana <ana@empresa.test>\nTo: c@cliente.test\nSubject: Oferta\nDate: Mon, 1 Sep 2026 10:00:00 +0000\nMessage-ID: <o1@empresa.test>\n\nPropuesta\n")
	put("INBOX", "From: Cliente <c@cliente.test>\nTo: ana@empresa.test\nSubject: Re: Oferta\nDate: Mon, 1 Sep 2026 11:00:00 +0000\nMessage-ID: <c1@cliente.test>\nIn-Reply-To: <o1@empresa.test>\n\nDe acuerdo\n")
	_ = mb.Close()

	token, _, err := env.svc.Login(ctx, mailbox, password, "203.0.113.7", "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := env.svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := domain.NewListQuery(1, 10, "")
	page, err := env.svc.ListMessages(ctx, sess, "INBOX", q)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("INBOX: %+v %v", page, err)
	}
	conv, err := env.svc.Conversation(ctx, sess, "INBOX", page.Items[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 2 || conv[0].Folder != "Sent" || conv[0].MessageID != "o1@empresa.test" || conv[1].MessageID != "c1@cliente.test" {
		t.Fatalf("la conversacion debe traer el mensaje propio de Enviados: %+v", conv)
	}
}
