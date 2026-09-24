//go:build integration

package app_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Posponer y seguimiento de punta a punta contra el IMAP en memoria: el trabajador reclama las filas
// de memReminders (todas las pendientes, sin mirar la hora) y actua sobre el buzon real.
func TestIntegracionPosponerYVolver(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	svc := env.svc
	seed(t, env.store)
	sess := loginIntegration(t, svc)
	if _, err := svc.ReadMessage(ctx, sess, "INBOX", 1, true, false); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Snooze(ctx, sess, "INBOX", []uint32{1, 2}, time.Now().Add(time.Hour))
	if err != nil || len(res.Snoozed) != 2 || len(res.Failed) != 0 {
		t.Fatalf("posponer: %+v %v", res, err)
	}
	if !hasFolder(t, svc, sess, domain.SnoozedFolderName) {
		t.Fatal("la carpeta de pospuestos se crea al primer uso")
	}
	waiting := listAll(t, svc, sess, domain.SnoozedFolderName)
	if len(waiting) != 2 || len(listAll(t, svc, sess, "INBOX")) != 1 {
		t.Fatalf("esperan en Snoozed: %+v", waiting)
	}
	first := res.Snoozed[0]
	if first.MessageID != "msg1@x.test" || first.UIDValidity == 0 || first.UID == 0 || first.ReturnFolder != "INBOX" || first.Subject != "Hola" ||
		strings.Join(first.Addresses, ",") != "luis@x.test" {
		t.Fatalf("fila con su referencia IMAP: %+v", first)
	}
	folders, err := svc.Folders(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.Name == domain.SnoozedFolderName && f.Role != domain.RoleSnoozed {
			t.Fatalf("papel de la carpeta: %+v", f)
		}
	}

	// El segundo lo saca el usuario a mano antes de su hora: su fila se cierra sin tocarlo.
	if err := svc.Move(ctx, sess, domain.SnoozedFolderName, res.Snoozed[1].UID, "Archive"); err != nil {
		t.Fatal(err)
	}
	env.reminders.run(t, svc)

	if out := env.reminders.outcome(first.ID); out.Status != domain.ReminderDone || out.Result != domain.ReminderReturned {
		t.Fatalf("vuelve: %+v", out)
	}
	if out := env.reminders.outcome(res.Snoozed[1].ID); out.Result != domain.ReminderMissing {
		t.Fatalf("movido por el usuario: %+v", out)
	}
	inbox := listAll(t, svc, sess, "INBOX")
	var back *domain.Envelope
	for i := range inbox {
		if inbox[i].Subject == "Hola" {
			back = &inbox[i]
		}
	}
	if back == nil || hasFlag(back.Flags, domain.FlagSeen) {
		t.Fatalf("vuelve a INBOX sin leer: %+v", inbox)
	}
	archived := listAll(t, svc, sess, "Archive")
	if len(archived) != 1 || archived[0].Subject != "Novedades" || len(listAll(t, svc, sess, domain.SnoozedFolderName)) != 0 {
		t.Fatalf("el movido sigue donde lo dejo el usuario: %+v", archived)
	}
}

func TestIntegracionDevolverYaUnPospuesto(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	svc := env.svc
	seed(t, env.store)
	sess := loginIntegration(t, svc)
	res, err := svc.Snooze(ctx, sess, "INBOX", []uint32{3}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Unsnooze(ctx, sess, res.Snoozed[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(listAll(t, svc, sess, "INBOX")) != 3 || len(listAll(t, svc, sess, domain.SnoozedFolderName)) != 0 {
		t.Fatal("el mensaje sin Message-ID vuelve por su UID")
	}
	if env.reminders.row(res.Snoozed[0].ID).Status != domain.ReminderCanceled {
		t.Fatal("la fila se cancela")
	}
}

func TestIntegracionSeguimiento(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	svc := env.svc
	sess := loginIntegration(t, svc)
	send := func(subject string) string {
		t.Helper()
		res, err := svc.Send(ctx, sess, domain.Draft{To: []domain.Address{{Email: "cliente@x.test"}}, Subject: subject, Text: "cuerpo"},
			domain.SendOptions{IdempotencyKey: "integracion-seguimiento-" + strings.ToLower(subject)})
		if err != nil || !res.SavedToSent {
			t.Fatalf("enviar %s: %+v %v", subject, res, err)
		}
		return res.MessageID
	}
	quiet := send("Presupuesto")
	answered := send("Contrato")
	sent := listAll(t, svc, sess, "Sent")
	if len(sent) != 2 {
		t.Fatalf("enviados: %+v", sent)
	}

	quietRow, err := svc.CreateFollowUp(ctx, sess, app.FollowUpRequest{MessageID: quiet, Subject: "Presupuesto", Recipients: []string{"cliente@x.test"}, Days: 3})
	if err != nil || quietRow.Folder != "Sent" || quietRow.UID == 0 || quietRow.UIDValidity == 0 {
		t.Fatalf("seguimiento: %+v %v", quietRow, err)
	}
	answeredRow, err := svc.CreateFollowUp(ctx, sess, app.FollowUpRequest{MessageID: answered, Days: 3})
	if err != nil {
		t.Fatal(err)
	}
	// La respuesta del cliente llega y el usuario la archiva: cuenta igual.
	mb, err := env.store.Open(ctx, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	reply := fmt.Sprintf("From: cliente@x.test\r\nTo: %s\r\nSubject: Re: Contrato\r\nMessage-ID: <resp@x.test>\r\nIn-Reply-To: <%s>\r\nReferences: <%s>\r\n\r\nDe acuerdo\r\n",
		mailbox, answered, answered)
	if _, err := mb.Append(ctx, "Archive", []byte(reply), nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	mb.Close()

	env.reminders.run(t, svc)
	if out := env.reminders.outcome(answeredRow.ID); out.Status != domain.ReminderDone || out.Result != domain.ReminderReplied {
		t.Fatalf("con respuesta: %+v", out)
	}
	if out := env.reminders.outcome(quietRow.ID); out.Status != domain.ReminderDone || out.Result != domain.ReminderReminded {
		t.Fatalf("sin respuesta: %+v", out)
	}
	inbox := listAll(t, svc, sess, "INBOX")
	if len(inbox) != 1 || inbox[0].Subject != "Presupuesto" || !hasFlag(inbox[0].Flags, domain.FlagFlagged) || hasFlag(inbox[0].Flags, domain.FlagSeen) {
		t.Fatalf("aviso en INBOX con \\Flagged y sin \\Seen: %+v", inbox)
	}
	if len(env.smtp.received()) != 2 {
		t.Fatal("el seguimiento no envia nada")
	}

	// Si el enviado ya no esta (el usuario lo borro), se cierra sin avisar.
	gone := send("Borrado")
	goneRow, err := svc.CreateFollowUp(ctx, sess, app.FollowUpRequest{MessageID: gone, Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listAll(t, svc, sess, "Sent") {
		if e.Subject == "Borrado" {
			if err := svc.Move(ctx, sess, "Sent", e.UID, "Trash"); err != nil {
				t.Fatal(err)
			}
		}
	}
	env.reminders.run(t, svc)
	if out := env.reminders.outcome(goneRow.ID); out.Result != domain.ReminderMissing || len(listAll(t, svc, sess, "INBOX")) != 1 {
		t.Fatalf("enviado borrado: %+v", out)
	}
}

// memReminders hace de indice de recordatorios de mail-directory: reclamar pasa las pendientes a
// running sin mirar la hora (la prueba decide cuando corre el trabajador).
type memReminders struct {
	mu       sync.Mutex
	rows     map[string]*domain.Reminder
	users    map[string]string
	outcomes map[string]domain.ReminderOutcome
	seq      int
}

func newMemReminders() *memReminders {
	return &memReminders{rows: map[string]*domain.Reminder{}, users: map[string]string{}, outcomes: map[string]domain.ReminderOutcome{}}
}

// run corre el trabajador hasta que no queda ninguna pendiente.
func (m *memReminders) run(t *testing.T, svc *app.Service) {
	t.Helper()
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.RunReminders(runCtx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for m.pending() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("el trabajador no cerro los recordatorios")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	<-done
}

func (m *memReminders) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.rows {
		if r.Status == domain.ReminderPending || r.Status == domain.ReminderRunning {
			n++
		}
	}
	return n
}

func (m *memReminders) row(id string) domain.Reminder {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.rows[id]
}

func (m *memReminders) outcome(id string) domain.ReminderOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[id]
}

func (m *memReminders) CreateReminder(_ context.Context, in domain.NewReminder) (domain.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", m.seq)
	r := &domain.Reminder{
		ID: id, Kind: in.Kind, MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity, UID: in.UID,
		ReturnFolder: in.ReturnFolder, Subject: in.Subject, Addresses: in.Addresses, DueAt: in.DueAt, Status: domain.ReminderPending,
	}
	m.rows[id], m.users[id] = r, in.Username
	return *r, nil
}

func (m *memReminders) ListReminders(_ context.Context, username string, kind domain.ReminderKind) ([]domain.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Reminder{}
	for id, r := range m.rows {
		if m.users[id] == username && r.Kind == kind && (r.Status == domain.ReminderPending || r.Status == domain.ReminderRunning) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memReminders) RescheduleReminder(_ context.Context, _, id string, at time.Time) (domain.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return domain.Reminder{}, domain.ErrReminderNotFound
	}
	r.DueAt = at
	return *r, nil
}

func (m *memReminders) CancelReminder(_ context.Context, _, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return domain.ErrReminderNotFound
	}
	r.Status = domain.ReminderCanceled
	return nil
}

func (m *memReminders) ClaimReminders(_ context.Context, limit int, _ time.Duration) ([]domain.ReminderClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ReminderClaim
	for id, r := range m.rows {
		if len(out) < limit && r.Status == domain.ReminderPending {
			r.Status = domain.ReminderRunning
			out = append(out, domain.ReminderClaim{Reminder: *r, Username: m.users[id]})
		}
	}
	return out, nil
}

func (m *memReminders) FinishReminder(_ context.Context, id string, outcome domain.ReminderOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok || r.Status != domain.ReminderRunning {
		return domain.ErrReminderNotClaimed
	}
	r.Status = outcome.Status
	m.outcomes[id] = outcome
	return nil
}

func (m *memReminders) QuickReplies(context.Context, string) (domain.QuickReplyList, error) {
	return domain.QuickReplyList{}, nil
}

func (m *memReminders) CreateQuickReply(_ context.Context, _ string, q domain.QuickReply) (domain.QuickReply, error) {
	return q, nil
}

func (m *memReminders) UpdateQuickReply(_ context.Context, _ string, q domain.QuickReply) (domain.QuickReply, error) {
	return q, nil
}

func (m *memReminders) DeleteQuickReply(context.Context, string, string) error { return nil }
