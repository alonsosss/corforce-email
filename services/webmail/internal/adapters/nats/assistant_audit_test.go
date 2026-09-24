package nats

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type capturePublisher struct {
	subject string
	evt     events.Event
}

func (p *capturePublisher) PublishPersistent(subject string, evt events.Event) error {
	p.subject, p.evt = subject, evt
	return nil
}

func TestApunteDelAsistenteSinContenido(t *testing.T) {
	pub := &capturePublisher{}
	rec := domain.AssistantUsageRecord{
		TenantID: "11111111-1111-4111-8111-111111111111", MailboxID: "22222222-2222-4222-8222-222222222222",
		Username: "ana@empresa.pe", Action: domain.AssistantReply, Outcome: domain.AssistantOutcomeOK, Model: "claude-haiku-4-5",
		InputChars: 120, OutputChars: 40, InputTokens: 60, OutputTokens: 20, Messages: 1,
		At: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
	}
	if err := NewAssistantAudit(pub).AssistantUsed(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if pub.subject != SubjectAssistantUsed || pub.evt.Type != SubjectAssistantUsed || pub.evt.TenantID != rec.TenantID || pub.evt.UserID != rec.MailboxID {
		t.Fatalf("evento: %s %+v", pub.subject, pub.evt)
	}
	data := pub.evt.Data.(map[string]interface{})
	want := map[string]bool{"tenant_id": true, "mailbox_id": true, "username": true, "action": true, "outcome": true, "model": true,
		"input_chars": true, "output_chars": true, "input_tokens": true, "output_tokens": true, "messages": true, "at": true}
	for k := range data {
		if !want[k] {
			t.Fatalf("campo no previsto en el apunte: %s", k)
		}
	}
	if len(data) != len(want) || data["at"] != "2026-09-24T10:00:00Z" {
		t.Fatalf("payload: %v", data)
	}
}

func TestApunteSinBusEsUnError(t *testing.T) {
	if err := NewAssistantAudit(nil).AssistantUsed(context.Background(), domain.AssistantUsageRecord{}); err == nil {
		t.Fatal("sin bus el uso debe contarse como sin apunte")
	}
}
