package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func snoozeReq(username string, at time.Time) CreateReminderRequest {
	return CreateReminderRequest{
		Username: username, Kind: domain.ReminderSnooze, MessageID: uuid.NewString() + "@acme.test", Folder: "Snoozed",
		UIDValidity: 7, UID: 3, ReturnFolder: "INBOX", DueAt: at, Subject: "Factura", Addresses: []string{"proveedor@otro.example"},
	}
}

func followUpReq(username string, at time.Time) CreateReminderRequest {
	return CreateReminderRequest{
		Username: username, Kind: domain.ReminderFollowUp, MessageID: uuid.NewString() + "@acme.test", Folder: "Sent",
		DueAt: at, Subject: "Presupuesto", Addresses: []string{"cliente@otro.example"},
	}
}

func TestRecordatoriosDelBuzon(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	h.addMailbox(uuid.New(), "luis@otra.test", 0)
	ctx := context.Background()

	s, err := h.uc.CreateReminder(ctx, snoozeReq("Ana@acme.test", h.clock.Add(time.Hour)))
	if err != nil || s.TenantID != tenant || s.Username != "ana@acme.test" || s.Status != domain.ReminderPending || s.Kind != domain.ReminderSnooze {
		t.Fatalf("pospuesto: %+v %v", s, err)
	}
	f, err := h.uc.CreateReminder(ctx, followUpReq("ana@acme.test", h.clock.Add(72*time.Hour)))
	if err != nil || f.UID != 0 || f.Kind != domain.ReminderFollowUp {
		t.Fatalf("seguimiento sin UID (envio programado): %+v %v", f, err)
	}
	dup := followUpReq("ana@acme.test", h.clock.Add(time.Hour))
	dup.MessageID = f.MessageID
	if _, err := h.uc.CreateReminder(ctx, dup); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("seguimiento repetido del mismo mensaje: %v", err)
	}

	snoozed, err := h.uc.ListReminders(ctx, "ana@acme.test", domain.ReminderSnooze)
	if err != nil || len(snoozed) != 1 || snoozed[0].ID != s.ID {
		t.Fatalf("listado por tipo: %+v %v", snoozed, err)
	}
	if _, err := h.uc.ListReminders(ctx, "ana@acme.test", "otro"); fieldOf(t, err) != "kind" {
		t.Fatal("tipo invalido en el listado")
	}
	if other, _ := h.uc.ListReminders(ctx, "luis@otra.test", domain.ReminderSnooze); len(other) != 0 {
		t.Fatalf("otro buzon ve los recordatorios ajenos: %+v", other)
	}
	if _, err := h.uc.RescheduleReminder(ctx, "luis@otra.test", s.ID, h.clock.Add(2*time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reprogramar uno ajeno: %v", err)
	}
	moved, err := h.uc.RescheduleReminder(ctx, "ana@acme.test", s.ID, h.clock.Add(2*time.Hour))
	if err != nil || !moved.DueAt.Equal(h.clock.Add(2*time.Hour)) {
		t.Fatalf("reprogramar: %+v %v", moved, err)
	}
	if _, err := h.uc.RescheduleReminder(ctx, "ana@acme.test", s.ID, h.clock.Add(-time.Hour)); fieldOf(t, err) != "due_at" {
		t.Fatal("hora pasada al reprogramar")
	}
	if err := h.uc.CancelReminder(ctx, "ana@acme.test", s.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.CancelReminder(ctx, "ana@acme.test", s.ID); err != nil {
		t.Fatalf("cancelar dos veces: %v", err)
	}
	if _, err := h.uc.RescheduleReminder(ctx, "ana@acme.test", s.ID, h.clock.Add(3*time.Hour)); !errors.Is(err, domain.ErrReminderNotPending) {
		t.Fatalf("reprogramar uno cancelado: %v", err)
	}
}

func TestRecordatorioValidaLaFila(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	cases := map[string]func(*CreateReminderRequest){
		"kind":          func(r *CreateReminderRequest) { r.Kind = "alarma" },
		"uid":           func(r *CreateReminderRequest) { r.UID, r.UIDValidity = 0, 0 },
		"return_folder": func(r *CreateReminderRequest) { r.ReturnFolder = "" },
		"folder":        func(r *CreateReminderRequest) { r.Folder = "a\r\nb" },
		"message_id":    func(r *CreateReminderRequest) { r.MessageID = "con espacio@x" },
		"due_at":        func(r *CreateReminderRequest) { r.DueAt = h.clock.AddDate(0, 0, domain.MaxReminderDays+1) },
		"addresses[0]":  func(r *CreateReminderRequest) { r.Addresses = []string{"sin-arroba"} },
	}
	for field, change := range cases {
		req := snoozeReq("ana@acme.test", h.clock.Add(time.Hour))
		change(&req)
		if _, err := h.uc.CreateReminder(ctx, req); fieldOf(t, err) != field {
			t.Fatalf("%s: %v", field, err)
		}
	}
	f := followUpReq("ana@acme.test", h.clock.Add(time.Hour))
	f.MessageID = ""
	if _, err := h.uc.CreateReminder(ctx, f); fieldOf(t, err) != "message_id" {
		t.Fatal("seguimiento sin Message-ID")
	}
	f = followUpReq("ana@acme.test", h.clock.Add(time.Hour))
	f.ReturnFolder = "INBOX"
	if _, err := h.uc.CreateReminder(ctx, f); fieldOf(t, err) != "return_folder" {
		t.Fatal("seguimiento con carpeta de vuelta")
	}
	if _, err := h.uc.CreateReminder(ctx, snoozeReq("nadie@acme.test", h.clock.Add(time.Hour))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon inexistente: %v", err)
	}
}

func TestRecordatoriosTopePorBuzon(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	for i := 0; i < domain.MaxRemindersPerMailbox; i++ {
		if _, err := h.uc.CreateReminder(ctx, followUpReq("ana@acme.test", h.clock.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.uc.CreateReminder(ctx, snoozeReq("ana@acme.test", h.clock.Add(time.Hour))); !errors.Is(err, domain.ErrReminderLimit) {
		t.Fatalf("tope: %v", err)
	}
}

func TestReclamarYCerrarRecordatorios(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	s, err := h.uc.CreateReminder(ctx, snoozeReq("ana@acme.test", h.clock.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if claimed, _ := h.uc.ClaimReminders(ctx, 0, 0); len(claimed) != 0 {
		t.Fatal("reclamo uno que no vencio")
	}
	if _, err := h.uc.ClaimReminders(ctx, 1000, 0); fieldOf(t, err) != "limit" {
		t.Fatal("lote fuera de rango")
	}
	for attempt := 1; attempt <= domain.MaxReminderAttempts; attempt++ {
		h.clock = h.clock.Add(2 * time.Hour)
		claimed, err := h.uc.ClaimReminders(ctx, 10, 60)
		if err != nil || len(claimed) != 1 || claimed[0].Attempts != attempt || claimed[0].Status != domain.ReminderRunning {
			t.Fatalf("intento %d: %+v %v", attempt, claimed, err)
		}
		if err := h.uc.CancelReminder(ctx, "ana@acme.test", s.ID); !errors.Is(err, domain.ErrReminderNotPending) {
			t.Fatalf("cancelar uno en curso: %v", err)
		}
		closed, err := h.uc.FinishReminder(ctx, s.ID, domain.ReminderOutcome{Status: domain.ReminderFailed, Error: "imap\nno disponible", Retry: true})
		if err != nil || strings.Contains(closed.LastError, "\n") {
			t.Fatalf("cierre %d: %+v %v", attempt, closed, err)
		}
		want := domain.ReminderPending
		if attempt == domain.MaxReminderAttempts {
			want = domain.ReminderFailed
		}
		if closed.Status != want {
			t.Fatalf("intento %d: estado %s", attempt, closed.Status)
		}
	}
	if _, err := h.uc.FinishReminder(ctx, s.ID, domain.ReminderOutcome{Status: domain.ReminderDone, Result: domain.ReminderReturned}); !errors.Is(err, domain.ErrReminderNotClaimed) {
		t.Fatalf("cerrar uno no reclamado: %v", err)
	}
	if err := h.uc.CancelReminder(ctx, "ana@acme.test", s.ID); err != nil {
		t.Fatalf("retirar uno fallido: %v", err)
	}
}

func TestCerrarRecordatorioConSuResultado(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	f, err := h.uc.CreateReminder(ctx, followUpReq("ana@acme.test", h.clock.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(time.Hour)
	if claimed, err := h.uc.ClaimReminders(ctx, 5, 60); err != nil || len(claimed) != 1 {
		t.Fatalf("reclamar: %+v %v", claimed, err)
	}
	if _, err := h.uc.FinishReminder(ctx, f.ID, domain.ReminderOutcome{Status: domain.ReminderDone, Result: domain.ReminderReturned}); fieldOf(t, err) != "result" {
		t.Fatal("un seguimiento no termina como returned")
	}
	if _, err := h.uc.FinishReminder(ctx, f.ID, domain.ReminderOutcome{Status: domain.ReminderDone}); fieldOf(t, err) != "result" {
		t.Fatal("done sin resultado")
	}
	if _, err := h.uc.FinishReminder(ctx, f.ID, domain.ReminderOutcome{Status: domain.ReminderCanceled, Retry: true}); fieldOf(t, err) != "retry" {
		t.Fatal("reintento de un cancelado")
	}
	done, err := h.uc.FinishReminder(ctx, f.ID, domain.ReminderOutcome{Status: domain.ReminderDone, Result: domain.ReminderReminded})
	if err != nil || done.Status != domain.ReminderDone || done.Result != domain.ReminderReminded || done.DoneAt == nil {
		t.Fatalf("cierre: %+v %v", done, err)
	}
	if list, _ := h.uc.ListReminders(ctx, "ana@acme.test", domain.ReminderFollowUp); len(list) != 0 {
		t.Fatalf("un hecho sigue en la lista: %+v", list)
	}
}

func TestRespuestasRapidasDelBuzon(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	h.addMailbox(uuid.New(), "luis@otra.test", 0)
	ctx := context.Background()

	q, err := h.uc.CreateQuickReply(ctx, "Ana@acme.test", QuickReplyInput{Name: "  Gracias \t por  escribir ", HTML: "<p>Hola {nombre}</p>", Text: "Hola {nombre}\r\n"})
	if err != nil || q.Name != "Gracias por escribir" || q.Username != "ana@acme.test" || q.Text != "Hola {nombre}" {
		t.Fatalf("crear: %+v %v", q, err)
	}
	if _, err := h.uc.CreateQuickReply(ctx, "ana@acme.test", QuickReplyInput{Name: "gracias POR escribir", Text: "otra"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("nombre repetido: %v", err)
	}
	if _, err := h.uc.CreateQuickReply(ctx, "ana@acme.test", QuickReplyInput{Name: "Vacia"}); fieldOf(t, err) != "html" {
		t.Fatal("sin contenido")
	}
	if _, err := h.uc.CreateQuickReply(ctx, "ana@acme.test", QuickReplyInput{Name: strings.Repeat("n", domain.MaxQuickReplyNameRunes+1), Text: "x"}); fieldOf(t, err) != "name" {
		t.Fatal("nombre largo")
	}
	if _, err := h.uc.CreateQuickReply(ctx, "ana@acme.test", QuickReplyInput{Name: "Larga", Text: strings.Repeat("x", domain.MaxQuickReplyTextBytes+1)}); fieldOf(t, err) != "text" {
		t.Fatal("texto largo")
	}
	if _, err := h.uc.UpdateQuickReply(ctx, "luis@otra.test", q.ID, QuickReplyInput{Name: "Mia", Text: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cambiar una ajena: %v", err)
	}
	updated, err := h.uc.UpdateQuickReply(ctx, "ana@acme.test", q.ID, QuickReplyInput{Name: "Agradecer", Text: "Gracias, {nombre}"})
	if err != nil || updated.Name != "Agradecer" || updated.HTML != "" {
		t.Fatalf("cambiar: %+v %v", updated, err)
	}
	list, err := h.uc.QuickReplies(ctx, "ana@acme.test")
	if err != nil || len(list) != 1 || list[0].Name != "Agradecer" {
		t.Fatalf("listado: %+v %v", list, err)
	}
	if other, _ := h.uc.QuickReplies(ctx, "luis@otra.test"); len(other) != 0 {
		t.Fatalf("otro buzon ve respuestas ajenas: %+v", other)
	}
	if err := h.uc.DeleteQuickReply(ctx, "luis@otra.test", q.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar una ajena: %v", err)
	}
	if err := h.uc.DeleteQuickReply(ctx, "ana@acme.test", q.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRespuestasRapidasTopePorBuzon(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	for i := 0; i < domain.MaxQuickReplies; i++ {
		if _, err := h.uc.CreateQuickReply(ctx, "ana@acme.test", QuickReplyInput{Name: uuid.NewString(), Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.uc.CreateQuickReply(ctx, "ana@acme.test", QuickReplyInput{Name: "Una mas", Text: "x"}); !errors.Is(err, domain.ErrQuickReplyLimit) {
		t.Fatalf("tope: %v", err)
	}
}

func TestBorrarElBuzonBorraRecordatoriosYRespuestas(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()
	if _, err := h.uc.CreateReminder(ctx, snoozeReq(m.Username, h.clock.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.CreateQuickReply(ctx, m.Username, QuickReplyInput{Name: "Hola", Text: "Hola"}); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.DeleteMailbox(ctx, tenant, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.reminders.items) != 0 || len(h.quickReplies.items) != 0 {
		t.Fatalf("quedaron filas: recordatorios=%d respuestas=%d", len(h.reminders.items), len(h.quickReplies.items))
	}
}
