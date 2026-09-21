package outbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type recorder struct {
	subjects []string
	payloads []map[string]any
	raw      []string
}

func (r *recorder) Exec(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	r.subjects = append(r.subjects, args[1].(string))
	raw := string(args[3].([]byte))
	r.raw = append(r.raw, raw)
	var evt map[string]any
	_ = json.Unmarshal([]byte(raw), &evt)
	r.payloads = append(r.payloads, evt)
	return pgconn.CommandTag{}, nil
}

func sampleJob(status domain.Status) *domain.Job {
	return &domain.Job{
		ID: uuid.New(), TenantID: uuid.New(), MailboxID: uuid.New(), MailboxUsername: "ana@acme.test",
		SourceHost: "imap.origen.example", SourcePort: 993, SourceTLS: domain.TLSImplicit, SourceUsername: "ana@origen.example",
		SourcePasswordEnc: []byte("cifrado"), Status: status, Attempt: 1, RequestedBy: uuid.New(),
		Progress:  domain.Progress{MessagesCopied: 9, MessagesSkipped: 2, MessagesFailed: 1, BytesCopied: 1234, FoldersDone: 3},
		LastError: &domain.JobError{Code: domain.CodeQuotaExceeded, Message: "cuota"},
	}
}

func TestLosEventosLlevanAlActorYNuncaLaCredencialNiElUsuarioDeOrigen(t *testing.T) {
	rec := &recorder{}
	p := NewPublisher(rec)
	ctx := context.Background()
	j := sampleJob(domain.StatusPending)
	actor := uuid.New()

	if err := p.Created(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := p.Started(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := p.CancelRequested(ctx, j, actor); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.Status{domain.StatusSucceeded, domain.StatusFailed, domain.StatusCancelled} {
		j.Status = status
		if err := p.Finished(ctx, j, uuid.Nil); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{SubjectCreated, SubjectStarted, SubjectCancelRequested, SubjectCompleted, SubjectFailed, SubjectCancelled}
	if strings.Join(rec.subjects, ",") != strings.Join(want, ",") {
		t.Fatalf("subjects %v, se esperaba %v", rec.subjects, want)
	}
	for i, raw := range rec.raw {
		for _, leak := range []string{"cifrado", "ana@origen.example", "password"} {
			if strings.Contains(raw, leak) {
				t.Errorf("%s filtra %q: %s", rec.subjects[i], leak, raw)
			}
		}
		if rec.payloads[i]["tenant_id"] != j.TenantID.String() || rec.payloads[i]["source"] != "mail-migration" {
			t.Errorf("%s: envelope %v", rec.subjects[i], rec.payloads[i])
		}
	}
	if rec.payloads[0]["user_id"] != j.RequestedBy.String() {
		t.Errorf("created no lleva a quien lo lanzo: %v", rec.payloads[0])
	}
	if rec.payloads[2]["user_id"] != actor.String() {
		t.Errorf("cancel_requested no lleva a quien cancelo: %v", rec.payloads[2])
	}
	if _, ok := rec.payloads[3]["user_id"]; ok {
		t.Errorf("un cierre del ejecutor no tiene actor: %v", rec.payloads[3])
	}
	data, _ := rec.payloads[0]["data"].(map[string]any)
	if data["source_host"] != "imap.origen.example" || data["mailbox_username"] != "ana@acme.test" {
		t.Errorf("created sin el origen ni el buzon: %v", data)
	}
	closed, _ := rec.payloads[4]["data"].(map[string]any)
	if closed["error_code"] != string(domain.CodeQuotaExceeded) || closed["messages_copied"] != float64(9) {
		t.Errorf("failed sin resultado: %v", closed)
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.payloads[0]["timestamp"].(string)); err != nil {
		t.Errorf("timestamp: %v", err)
	}
}
