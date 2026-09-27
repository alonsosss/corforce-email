package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func line(t *testing.T, program, message string) MailLogLine {
	t.Helper()
	l, err := ParseMailLogLine(`{"time":"1790466052","program":"` + program + `","priority":"info","message":` + quote(message) + `}`)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Lineas tomadas del registro real de produccion (2026-09-26).
func TestClasificaLineasRealesDePostfix(t *testing.T) {
	cases := []struct {
		program, message string
		want             LogRecord
	}{
		{"postfix/submission/smtpd", "C9E8C60485: client=ec2-23-22-171-91.compute-1.amazonaws.com[23.22.171.91], sasl_method=PLAIN, sasl_username=Atencion@campovivoalimentos.com",
			LogRecord{Kind: LogClient, QID: "C9E8C60485", ClientIP: "23.22.171.91", SASLUsername: "atencion@campovivoalimentos.com"}},
		{"postfix/cleanup", "C9E8C60485: message-id=<991dcb7f-9115-4584-9b75-4867e8676b35@campovivoalimentos.com>",
			LogRecord{Kind: LogMessageID, QID: "C9E8C60485", MessageID: "991dcb7f-9115-4584-9b75-4867e8676b35@campovivoalimentos.com"}},
		{"postfix/qmgr", "C9E8C60485: from=<atencion@campovivoalimentos.com>, size=1957, nrcpt=1 (queue active)",
			LogRecord{Kind: LogFrom, QID: "C9E8C60485", From: "atencion@campovivoalimentos.com"}},
		{"postfix/smtp", "C9E8C60485: to=<alonsosss21@gmail.com>, relay=gmail-smtp-in.l.google.com[142.251.127.27]:25, delay=2, delays=1.3/0.03/0.11/0.53, dsn=2.0.0, status=sent (250 2.0.0 OK  1790466053 ffacd0b85a97d-4887a35f875si13401924f8f.143 - gsmtp)",
			LogRecord{Kind: LogDelivery, QID: "C9E8C60485", To: "alonsosss21@gmail.com", Relay: "gmail-smtp-in.l.google.com[142.251.127.27]:25", Delay: "2", DSN: "2.0.0", Status: DeliverySent,
				Reason: "250 2.0.0 OK  1790466053 ffacd0b85a97d-4887a35f875si13401924f8f.143 - gsmtp"}},
		{"postfix/lmtp", "8212860485: to=<atencion@campovivoalimentos.com>, relay=dovecot[172.22.1.250]:24, delay=0.89, delays=0.84/0.01/0.01/0.02, dsn=2.0.0, status=sent (250 2.0.0 <atencion@campovivoalimentos.com> 7xcrEkZYuGpKYwAAIAM77A Saved)",
			LogRecord{Kind: LogDelivery, QID: "8212860485", To: "atencion@campovivoalimentos.com", Relay: "dovecot[172.22.1.250]:24", Delay: "0.89", DSN: "2.0.0", Status: DeliverySent,
				Reason: "250 2.0.0 <atencion@campovivoalimentos.com> 7xcrEkZYuGpKYwAAIAM77A Saved", Local: true}},
		{"postfix/smtp", "4B1C2D3E4F: to=<nadie@empresa.example>, orig_to=<alias@campovivoalimentos.com>, relay=mx.empresa.example[203.0.113.5]:25, delay=1.2, delays=0.1/0/0.5/0.6, dsn=5.1.1, status=bounced (host mx.empresa.example[203.0.113.5] said: 550 5.1.1 <nadie@empresa.example>: Recipient address rejected: User unknown (in reply to RCPT TO command))",
			LogRecord{Kind: LogDelivery, QID: "4B1C2D3E4F", To: "nadie@empresa.example", OrigTo: "alias@campovivoalimentos.com", Relay: "mx.empresa.example[203.0.113.5]:25", Delay: "1.2", DSN: "5.1.1", Status: DeliveryBounced,
				Reason: "host mx.empresa.example[203.0.113.5] said: 550 5.1.1 <nadie@empresa.example>: Recipient address rejected: User unknown (in reply to RCPT TO command)"}},
		{"postfix/smtp", "4B1C2D3E4F: to=<a@lento.example>, relay=none, delay=30, delays=0/0/30/0, dsn=4.4.1, status=deferred (connect to lento.example[198.51.100.7]:25: Connection timed out)",
			LogRecord{Kind: LogDelivery, QID: "4B1C2D3E4F", To: "a@lento.example", Relay: "none", Delay: "30", DSN: "4.4.1", Status: DeliveryDeferred,
				Reason: "connect to lento.example[198.51.100.7]:25: Connection timed out"}},
		{"postfix/cleanup", "4B1C2D3E4F: milter-reject: END-OF-MESSAGE from unknown[198.51.100.9]: 5.7.1 Spam message rejected; from=<spam@malo.example> to=<ventas@campovivoalimentos.com> proto=ESMTP helo=<malo>",
			LogRecord{Kind: LogReject, QID: "4B1C2D3E4F", Reason: "5.7.1 Spam message rejected", From: "spam@malo.example", To: "ventas@campovivoalimentos.com", Status: DeliveryRejected}},
		{"postfix/smtpd", "NOQUEUE: reject: RCPT from unknown[198.51.100.9]: 550 5.1.1 <x@campovivoalimentos.com>: Recipient address rejected: User unknown in virtual mailbox table; from=<a@b.example> to=<x@campovivoalimentos.com> proto=ESMTP helo=<b>",
			LogRecord{Kind: LogReject, Reason: "550 5.1.1 <x@campovivoalimentos.com>: Recipient address rejected: User unknown in virtual mailbox table", From: "a@b.example", To: "x@campovivoalimentos.com", Status: DeliveryRejected}},
		{"postfix/postscreen", "NOQUEUE: reject: RCPT from [198.51.100.9]:40000: 550 5.7.1 Service unavailable; client [198.51.100.9] blocked using zen.spamhaus.org; from=<a@b.example>, to=<ventas@campovivoalimentos.com>, proto=ESMTP, helo=<b>",
			LogRecord{Kind: LogReject, Reason: "550 5.7.1 Service unavailable; client [198.51.100.9] blocked using zen.spamhaus.org", From: "a@b.example", To: "ventas@campovivoalimentos.com", Status: DeliveryRejected}},
	}
	for _, tc := range cases {
		got, ok := ClassifyLogLine(line(t, tc.program, tc.message))
		if !ok || got != tc.want {
			t.Errorf("%s\n  obtuve %+v %v\n  quiero %+v", tc.message, got, ok, tc.want)
		}
	}
}

func TestIgnoraLoQueNoEsUnaEntrega(t *testing.T) {
	for _, tc := range []struct{ program, message string }{
		{"postfix/qmgr", "C9E8C60485: removed"},
		{"postfix/anvil", "statistics: max connection rate 1/60s for (smtp:1.2.3.4) at Sep 26 11:00:00"},
		{"postfix/postscreen", "CONNECT from [1.2.3.4]:5000 to [172.22.1.253]:25"},
		{"postfix/cleanup", "C9E8C60485: replace: header Received: from localhost"},
		{"postfix/smtp", "C9E8C60485: to=<a@b.example>, relay=x, delay=1, delays=1, dsn=2.0.0, status=inventado (x)"},
		{"dovecot", "C9E8C60485: to=<a@b.example>, relay=x, delay=1, delays=1/0/0/0, dsn=2.0.0, status=sent (250)"},
	} {
		if rec, ok := ClassifyLogLine(line(t, tc.program, tc.message)); ok {
			t.Errorf("%s: no aporta al registro y se clasifico como %+v", tc.message, rec)
		}
	}
	for _, raw := range []string{`no es json`, `{"time":"x","program":"postfix/smtp","message":"a"}`, `{"time":"1","program":"postfix/smtp","message":""}`} {
		if _, err := ParseMailLogLine(raw); err == nil {
			t.Errorf("%q: esperaba error", raw)
		}
	}
}

func TestEventoRecortaYDaHuellaEstable(t *testing.T) {
	tenant := uuid.New()
	l := line(t, "postfix/smtp", "x")
	rec := LogRecord{QID: "ABC123", To: "Ana@Example.COM", Status: DeliveryBounced, Reason: strings.Repeat("é", 800) + "\r\nX-Inyectada: 1", Delay: "NaN"}
	qc := QueueContext{From: "Ventas@Mentorenergy.uk", SASLUsername: "ventas@mentorenergy.uk", MessageID: "m@x"}
	e := NewDeliveryEvent(tenant, DirectionOutbound, rec, qc, l, "raw")
	if e.Sender != "ventas@mentorenergy.uk" || e.Recipient != "ana@example.com" || len(e.Reason) > MaxDeliveryReason ||
		strings.ContainsAny(e.Reason, "\r\n") || e.DelaySeconds != "" || e.MessageID != "m@x" {
		t.Fatalf("evento: %+v", e)
	}
	again := NewDeliveryEvent(tenant, DirectionOutbound, rec, qc, l, "raw")
	other := NewDeliveryEvent(tenant, DirectionInbound, rec, qc, l, "raw")
	if e.EventKey != again.EventKey || e.EventKey == other.EventKey {
		t.Error("la huella depende de la linea, la empresa y la direccion, y de nada mas")
	}
	if AddressDomain("a@B.example.") != "b.example" || AddressDomain("sin-arroba") != "" || AddressDomain("") != "" {
		t.Error("AddressDomain")
	}
}
