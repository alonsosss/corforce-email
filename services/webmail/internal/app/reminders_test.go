package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func newReminderHarness(t *testing.T) (*harness, domain.Session) {
	t.Helper()
	h := newHarness(t)
	h.mb.appendUID = 0
	h.mb.uidValidity = 9
	_, sess := h.login(t)
	return h, sess
}

func (h *harness) seedMessage(folder string, uid uint32, messageID string, raw string) {
	h.mb.put(folder, uid, &fakeMessage{raw: []byte(raw), messageID: messageID})
}

func (h *harness) runReminders(t *testing.T) {
	t.Helper()
	h.svc.processReminderBatch(context.Background())
}

func TestPosponerMueveARegistraYDevuelveSinLeer(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.seedMessage("INBOX", 5, "factura@proveedor.pe", "Subject: Factura\r\n\r\nx")
	at := h.clock.Now().Add(3 * time.Hour)

	res, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{5, 5, 6}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Snoozed) != 1 || len(res.Failed) != 1 || res.Failed[0] != 6 {
		t.Fatalf("resultado: %+v", res)
	}
	r := res.Snoozed[0]
	if r.Folder != domain.SnoozedFolderName || r.ReturnFolder != "INBOX" || r.UID == 0 || r.UIDValidity != 9 || r.MessageID != "factura@proveedor.pe" || !r.DueAt.Equal(at) {
		t.Fatalf("fila: %+v", r)
	}
	if strings.Join(h.mb.created, ",") != domain.SnoozedFolderName || h.mb.messages["INBOX"][5] != nil || h.mb.messages[domain.SnoozedFolderName][r.UID] == nil {
		t.Fatalf("el mensaje espera en Snoozed: creadas=%v %+v", h.mb.created, h.mb.messages)
	}

	h.runReminders(t)
	if out := h.reminders.finished[r.ID]; out.Status != domain.ReminderDone || out.Result != domain.ReminderReturned {
		t.Fatalf("cierre: %+v", out)
	}
	if len(h.mb.messages[domain.SnoozedFolderName]) != 0 || len(h.mb.messages["INBOX"]) != 1 {
		t.Fatalf("vuelve a su carpeta: %+v", h.mb.messages)
	}
	last := h.mb.flagged[len(h.mb.flagged)-1]
	if len(last.Remove) != 1 || last.Remove[0] != domain.FlagSeen || len(last.Add) != 0 {
		t.Fatalf("vuelve sin leer: %+v", last)
	}
}

func TestPospuestoQueElUsuarioMovioSeCierraSinTocarlo(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.seedMessage("INBOX", 5, "a@b.pe", "x")
	res, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{5}, h.clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Snoozed[0]
	if _, err := h.mb.Move(ctx, domain.SnoozedFolderName, []uint32{r.UID}, "Trash"); err != nil {
		t.Fatal(err)
	}
	moves, flags := len(h.mb.moved), len(h.mb.flagged)
	h.runReminders(t)
	if out := h.reminders.finished[r.ID]; out.Status != domain.ReminderDone || out.Result != domain.ReminderMissing {
		t.Fatalf("cierre: %+v", out)
	}
	if len(h.mb.moved) != moves || len(h.mb.flagged) != flags || len(h.mb.messages["Trash"]) != 1 {
		t.Fatal("un mensaje que el usuario saco de Snoozed no se toca")
	}
}

func TestPosponerRechazaCarpetasYDevuelveSiFallaElDirectorio(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	at := h.clock.Now().Add(time.Hour)
	h.mb.folders = append(h.mb.folders, domain.Folder{Name: domain.SnoozedFolderName, Role: domain.RoleSnoozed, Selectable: true},
		domain.Folder{Name: domain.ScheduledFolderName, Role: domain.RoleScheduled, Selectable: true})
	for _, folder := range []string{domain.SnoozedFolderName, domain.ScheduledFolderName} {
		var verr *domain.ValidationError
		if _, err := h.svc.Snooze(ctx, sess, folder, []uint32{1}, at); !errors.As(err, &verr) || verr.Field != "folder" {
			t.Fatalf("%s: %v", folder, err)
		}
	}
	if _, err := h.svc.Snooze(ctx, sess, "NoExiste", []uint32{1}, at); !errors.Is(err, domain.ErrFolderNotFound) {
		t.Fatalf("carpeta inexistente: %v", err)
	}
	var verr *domain.ValidationError
	if _, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{1}, h.clock.Now()); !errors.As(err, &verr) || verr.Field != "until" {
		t.Fatalf("hora inmediata: %v", err)
	}
	if _, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{1}, h.clock.Now().AddDate(0, 0, 31)); !errors.As(err, &verr) || verr.Field != "until" {
		t.Fatalf("hora lejana: %v", err)
	}

	h.seedMessage("INBOX", 5, "a@b.pe", "x")
	h.reminders.err = domain.ErrReminderLimit
	if _, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{5}, at); !errors.Is(err, domain.ErrReminderLimit) {
		t.Fatalf("tope del directorio: %v", err)
	}
	if len(h.mb.messages["INBOX"]) != 1 || len(h.mb.messages[domain.SnoozedFolderName]) != 0 {
		t.Fatalf("sin fila el mensaje vuelve a su carpeta: %+v", h.mb.messages)
	}
	h.reminders.err = errors.New("directorio caido")
	back, _ := h.mb.FindByMessageID(ctx, "INBOX", "a@b.pe")
	if _, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{back}, at); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("directorio caido: %v", err)
	}
}

func TestVolverAPosponerCancelaElAnterior(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.seedMessage("INBOX", 5, "a@b.pe", "x")
	first, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{5}, h.clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.mb.Move(ctx, domain.SnoozedFolderName, []uint32{first.Snoozed[0].UID}, "INBOX"); err != nil {
		t.Fatal(err)
	}
	uid, _ := h.mb.FindByMessageID(ctx, "INBOX", "a@b.pe")
	second, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{uid}, h.clock.Now().Add(5*time.Hour))
	if err != nil || len(second.Snoozed) != 1 {
		t.Fatalf("posponer otra vez: %+v %v", second, err)
	}
	if h.reminders.row(first.Snoozed[0].ID).Status != domain.ReminderCanceled {
		t.Fatal("el pospuesto anterior del mismo mensaje se cancela")
	}
}

func TestDevolverYaUnPospuesto(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.mb.folders = append(h.mb.folders, domain.Folder{Name: "Clientes", Selectable: true})
	h.seedMessage("Clientes", 5, "a@b.pe", "x")
	res, err := h.svc.Snooze(ctx, sess, "Clientes", []uint32{5}, h.clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Unsnooze(ctx, sess, res.Snoozed[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(h.mb.messages["Clientes"]) != 1 || h.reminders.row(res.Snoozed[0].ID).Status != domain.ReminderCanceled {
		t.Fatalf("vuelve a su carpeta y la fila se cancela: %+v", h.mb.messages)
	}
	if err := h.svc.Unsnooze(ctx, sess, res.Snoozed[0].ID); !errors.Is(err, domain.ErrReminderNotFound) {
		t.Fatalf("devolver dos veces: %v", err)
	}
	if err := h.svc.Unsnooze(ctx, sess, "no-es-uuid"); err == nil {
		t.Fatal("id invalido")
	}
}

func TestPospuestoVuelveALaEntradaSiSuCarpetaYaNoExiste(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.mb.folders = append(h.mb.folders, domain.Folder{Name: "Temporal", Selectable: true})
	h.seedMessage("Temporal", 5, "a@b.pe", "x")
	res, err := h.svc.Snooze(ctx, sess, "Temporal", []uint32{5}, h.clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h.mb.folders = h.mb.folders[:len(h.mb.folders)-2]
	h.mb.folders = append(h.mb.folders, domain.Folder{Name: domain.SnoozedFolderName, Role: domain.RoleSnoozed, Selectable: true})
	h.runReminders(t)
	if out := h.reminders.finished[res.Snoozed[0].ID]; out.Result != domain.ReminderReturned || len(h.mb.messages["INBOX"]) != 1 {
		t.Fatalf("vuelve a INBOX: %+v %+v", out, h.mb.messages)
	}
}

func sentMessage(id string) string {
	return "Message-ID: <" + id + ">\r\nSubject: Presupuesto\r\nid=" + id + ";\r\n\r\ncuerpo"
}

func TestSeguimientoSinRespuestaDejaElAvisoEnLaEntrada(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.seedMessage("Sent", 30, "presupuesto@empresa.pe", sentMessage("presupuesto@empresa.pe"))

	r, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "presupuesto@empresa.pe", Subject: "Presupuesto", Recipients: []string{"cliente@x.pe"}, Days: 3})
	if err != nil {
		t.Fatal(err)
	}
	if r.Folder != "Sent" || r.UID != 30 || r.UIDValidity != 9 || !r.DueAt.Equal(h.clock.Now().Add(72*time.Hour)) || r.Addresses[0] != "cliente@x.pe" {
		t.Fatalf("fila: %+v", r)
	}
	again, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "presupuesto@empresa.pe", Days: 3})
	if err != nil || again.ID != r.ID {
		t.Fatalf("repetir devuelve el activo: %+v %v", again, err)
	}

	h.runReminders(t)
	if out := h.reminders.finished[r.ID]; out.Status != domain.ReminderDone || out.Result != domain.ReminderReminded {
		t.Fatalf("cierre: %+v", out)
	}
	var copies []appended
	for _, a := range h.mb.appended {
		if a.folder == "INBOX" {
			copies = append(copies, a)
		}
	}
	if len(copies) != 1 || len(copies[0].flags) != 1 || copies[0].flags[0] != domain.FlagFlagged || !strings.Contains(string(copies[0].raw), "Presupuesto") {
		t.Fatalf("copia en INBOX con \\Flagged y sin \\Seen: %+v", copies)
	}

	// Un segundo intento (el cierre se perdio) no duplica la copia: la vuelve a marcar.
	h.reminders.rows[r.ID].Status = domain.ReminderPending
	h.runReminders(t)
	inbox := 0
	for _, a := range h.mb.appended {
		if a.folder == "INBOX" {
			inbox++
		}
	}
	last := h.mb.flagged[len(h.mb.flagged)-1]
	if inbox != 1 || last.Add[0] != domain.FlagFlagged || last.Remove[0] != domain.FlagSeen {
		t.Fatalf("reintento: copias=%d marca=%+v", inbox, last)
	}
}

func TestSeguimientoConRespuestaSeCierraEnSilencio(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.mb.folders = append(h.mb.folders, domain.Folder{Name: "Clientes", Selectable: true})
	h.seedMessage("Sent", 30, "p@empresa.pe", sentMessage("p@empresa.pe"))
	// Una respuesta propia en Enviados no cuenta; la del cliente, archivada en otra carpeta, si.
	h.seedMessage("Sent", 31, "mia@empresa.pe", "In-Reply-To: <p@empresa.pe>\r\n\r\nx")
	r, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "p@empresa.pe", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.runReminders(t)
	if out := h.reminders.finished[r.ID]; out.Result != domain.ReminderReminded {
		t.Fatalf("una respuesta propia no cuenta: %+v", out)
	}

	r2, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "q@empresa.pe", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.seedMessage("Sent", 40, "q@empresa.pe", sentMessage("q@empresa.pe"))
	h.seedMessage("Clientes", 7, "resp@cliente.pe", "References: <otro@x> <q@empresa.pe>\r\n\r\nde acuerdo")
	appends := len(h.mb.appended)
	h.runReminders(t)
	if out := h.reminders.finished[r2.ID]; out.Result != domain.ReminderReplied || len(h.mb.appended) != appends {
		t.Fatalf("con respuesta: %+v", out)
	}
}

func TestSeguimientoDeUnMensajeQueYaNoEstaOProgramado(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	// Programado: aun no esta en Enviados y la fila no lleva UID; al vencer, ya salio.
	r, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "luego@empresa.pe", Base: h.clock.Now().Add(time.Hour), Days: 2})
	if err != nil || r.UID != 0 || !r.DueAt.Equal(h.clock.Now().Add(49*time.Hour)) {
		t.Fatalf("seguimiento de un programado: %+v %v", r, err)
	}
	h.seedMessage("Sent", 50, "luego@empresa.pe", sentMessage("luego@empresa.pe"))
	gone, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "borrado@empresa.pe", Days: 2})
	if err != nil {
		t.Fatal(err)
	}
	h.runReminders(t)
	if out := h.reminders.finished[r.ID]; out.Result != domain.ReminderReminded {
		t.Fatalf("el programado ya salio: %+v", out)
	}
	if out := h.reminders.finished[gone.ID]; out.Result != domain.ReminderMissing {
		t.Fatalf("el enviado ya no esta: %+v", out)
	}
}

func TestSeguimientoValidaElPlazo(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	var verr *domain.ValidationError
	for _, days := range []int{0, -1, 31} {
		if err := h.svc.CheckFollowUp(days, time.Time{}); !errors.As(err, &verr) || verr.Field != "follow_up_days" {
			t.Fatalf("%d dias: %v", days, err)
		}
	}
	if err := h.svc.CheckFollowUp(20, h.clock.Now().AddDate(0, 0, 20)); !errors.As(err, &verr) {
		t.Fatalf("un programado lejano mas el plazo supera el tope: %v", err)
	}
	if _, err := h.svc.CreateFollowUp(ctx, sess, FollowUpRequest{MessageID: "sin arroba", Days: 1}); !errors.As(err, &verr) || verr.Field != "message_id" {
		t.Fatalf("Message-ID invalido: %v", err)
	}
}

func TestRecordatorioConElBuzonCaidoSeReintenta(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	h.seedMessage("INBOX", 5, "a@b.pe", "x")
	res, err := h.svc.Snooze(ctx, sess, "INBOX", []uint32{5}, h.clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h.mb.openErr = domain.ErrUnavailable
	h.runReminders(t)
	if out := h.reminders.finished[res.Snoozed[0].ID]; out.Status != domain.ReminderFailed || !out.Retry {
		t.Fatalf("reintento: %+v", out)
	}
	h.mb.openErr = nil
	h.runReminders(t)
	if out := h.reminders.finished[res.Snoozed[0].ID]; out.Result != domain.ReminderReturned {
		t.Fatalf("segundo intento: %+v", out)
	}
}

func TestRespuestasRapidasSeSanean(t *testing.T) {
	h, sess := newReminderHarness(t)
	ctx := context.Background()
	q, err := h.svc.CreateQuickReply(ctx, sess, domain.QuickReplyInput{Name: "Gracias", HTML: "<p>Hola {nombre}</p>"})
	if err != nil {
		t.Fatal(err)
	}
	in := h.reminders.quickIn[0]
	if in.HTML != "limpio:<p>Hola {nombre}</p>" || in.Text != "texto plano" || q.ID == "" {
		t.Fatalf("saneada con su texto: %+v", in)
	}
	var verr *domain.ValidationError
	if _, err := h.svc.CreateQuickReply(ctx, sess, domain.QuickReplyInput{Name: " ", HTML: "x"}); !errors.As(err, &verr) || verr.Field != "name" {
		t.Fatalf("sin nombre: %v", err)
	}
	if _, err := h.svc.CreateQuickReply(ctx, sess, domain.QuickReplyInput{Name: "Vacia", HTML: "  "}); !errors.As(err, &verr) || verr.Field != "html" {
		t.Fatalf("sin contenido: %v", err)
	}
	if _, err := h.svc.UpdateQuickReply(ctx, sess, "00000000-0000-4000-8000-000000000999", domain.QuickReplyInput{Name: "a", HTML: "b"}); !errors.Is(err, domain.ErrQuickReplyNotFound) {
		t.Fatalf("inexistente: %v", err)
	}
	if _, err := h.svc.UpdateQuickReply(ctx, sess, q.ID, domain.QuickReplyInput{Name: "Agradecer", HTML: "<p>Gracias</p>"}); err != nil {
		t.Fatal(err)
	}
	list, err := h.svc.QuickReplies(ctx, sess)
	if err != nil || len(list.Items) != 1 || list.Items[0].Name != "Agradecer" {
		t.Fatalf("listado: %+v %v", list, err)
	}
	if err := h.svc.DeleteQuickReply(ctx, sess, q.ID); err != nil {
		t.Fatal(err)
	}
	h.reminders.quickErr = domain.ErrQuickReplyLimit
	if _, err := h.svc.CreateQuickReply(ctx, sess, domain.QuickReplyInput{Name: "Otra", HTML: "x"}); !errors.Is(err, domain.ErrQuickReplyLimit) {
		t.Fatalf("tope: %v", err)
	}
	h.reminders.quickErr = errors.New("caido")
	if _, err := h.svc.QuickReplies(ctx, sess); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("directorio caido: %v", err)
	}
}

func TestNewExigeRecordatoriosYSuConfiguracion(t *testing.T) {
	h := newHarness(t)
	d := h.deps()
	d.Reminders = nil
	if _, err := New(d); err == nil {
		t.Fatal("sin recordatorios no arranca")
	}
	d = h.deps()
	d.Config.ReminderBatch = 0
	if _, err := New(d); err == nil {
		t.Fatal("lote cero")
	}
}
