package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func scheduledDraft() domain.Draft {
	return domain.Draft{
		To:      []domain.Address{{Email: "luis@x.pe"}},
		Bcc:     []domain.Address{{Email: "oculto@x.pe"}},
		Subject: "Informe",
		Text:    "hola",
	}
}

// newScheduledHarness prepara un arnes donde cada Append recibe un UID propio (el trabajador
// localiza el mensaje por UID y Message-ID) y el sobre que saca Finalize es el del borrador.
func newScheduledHarness(t *testing.T) (*harness, domain.Session) {
	t.Helper()
	h := newHarness(t)
	h.mb.appendUID = 0
	h.mb.uidValidity = 77
	h.composer.final = domain.FinalizedMessage{From: testUser, Recipients: []string{"luis@x.pe", "oculto@x.pe"}}
	_, sess := h.login(t)
	return h, sess
}

func (h *harness) schedule(t *testing.T, sess domain.Session, opts domain.SendOptions) domain.ScheduledResult {
	t.Helper()
	res, err := h.svc.Schedule(context.Background(), sess, scheduledDraft(), h.clock.Now().Add(time.Hour), opts)
	if err != nil {
		t.Fatalf("programar: %v", err)
	}
	return res
}

func (h *harness) claimOf(id string) domain.ScheduledClaim {
	row := h.directory.scheduled[id]
	return domain.ScheduledClaim{
		ID: row.ID, Username: testUser, MessageID: row.MessageID, Folder: row.Folder,
		UIDValidity: row.UIDValidity, UID: row.UID, SendAt: row.SendAt,
	}
}

func TestProgramarGuardaEnScheduledYRegistraLaFila(t *testing.T) {
	h, sess := newScheduledHarness(t)
	opts := sendOpts(0)
	res := h.schedule(t, sess, opts)
	if res.ID == "" || res.Replayed || !res.SendAt.Equal(h.clock.Now().Add(time.Hour)) {
		t.Fatalf("resultado: %+v", res)
	}
	if strings.Join(h.mb.created, ",") != domain.ScheduledFolderName {
		t.Fatalf("la carpeta Scheduled se crea si falta: %v", h.mb.created)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("programar no envia nada")
	}
	if len(h.mb.appended) != 1 || h.mb.appended[0].folder != "Scheduled" || !strings.HasPrefix(string(h.mb.appended[0].raw), "bcc=true;") {
		t.Fatalf("el mensaje guardado lleva el Bcc: %+v", h.mb.appended)
	}
	if f := h.mb.appended[0].flags; len(f) != 2 || f[0] != domain.FlagSeen || f[1] != domain.FlagDraft {
		t.Fatalf("flags: %v", f)
	}
	row := h.directory.scheduledNew[0]
	if row.Username != testUser || row.Folder != "Scheduled" || row.UIDValidity != 77 || row.UID == 0 || row.MessageID == "" ||
		row.Subject != "Informe" || strings.Join(row.Recipients, ",") != "luis@x.pe,oculto@x.pe" {
		t.Fatalf("fila: %+v", row)
	}

	// La misma clave no programa otra.
	again, err := h.svc.Schedule(context.Background(), sess, scheduledDraft(), h.clock.Now().Add(time.Hour), opts)
	if err != nil || !again.Replayed || again.ID != res.ID || len(h.directory.scheduledNew) != 1 {
		t.Fatalf("reintento: %+v %v", again, err)
	}
	// La misma clave con otra hora es otro mensaje.
	if _, err := h.svc.Schedule(context.Background(), sess, scheduledDraft(), h.clock.Now().Add(2*time.Hour), opts); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Fatalf("otra hora con la misma clave: %v", err)
	}
}

func TestProgramarValidaLaHoraAntesDeTocarNada(t *testing.T) {
	h, sess := newScheduledHarness(t)
	var verr *domain.ValidationError
	for _, at := range []time.Time{h.clock.Now(), h.clock.Now().Add(30 * time.Second), h.clock.Now().AddDate(0, 0, 31)} {
		if _, err := h.svc.Schedule(context.Background(), sess, scheduledDraft(), at, sendOpts(0)); !errors.As(err, &verr) || verr.Field != "send_at" {
			t.Errorf("%v: %v", at, err)
		}
	}
	if h.mail.opened != 0 || len(h.directory.scheduledNew) != 0 {
		t.Fatal("una hora invalida no abre el buzon ni registra nada")
	}
}

func TestProgramarAplicaLasReglasDeUnEnvio(t *testing.T) {
	h, sess := newScheduledHarness(t)
	d := scheduledDraft()
	d.From = domain.Address{Email: "director@empresa.pe"}
	if _, err := h.svc.Schedule(context.Background(), sess, d, h.clock.Now().Add(time.Hour), sendOpts(0)); !errors.Is(err, domain.ErrSenderNotAllowed) {
		t.Fatalf("remitente ajeno: %v", err)
	}
	d = scheduledDraft()
	d.Attachments = []domain.Attachment{{Filename: "virus.exe", Data: []byte("x")}}
	h.scanner.infect = "virus.exe"
	if _, err := h.svc.Schedule(context.Background(), sess, d, h.clock.Now().Add(time.Hour), sendOpts(0)); !errors.Is(err, domain.ErrAttachmentInfected) {
		t.Fatalf("adjunto infectado: %v", err)
	}
	if len(h.mb.appended) != 0 {
		t.Fatal("nada se guarda en Scheduled")
	}
}

func TestSiElDirectorioNoRegistraLaFilaElMensajeSeRetira(t *testing.T) {
	h, sess := newScheduledHarness(t)
	h.directory.createErr = fmt.Errorf("%w: mail-directory caido", domain.ErrUnavailable)
	opts := sendOpts(0)
	if _, err := h.svc.Schedule(context.Background(), sess, scheduledDraft(), h.clock.Now().Add(time.Hour), opts); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if len(h.mb.expunged) != 1 || !strings.HasPrefix(h.mb.expunged[0], "Scheduled:") {
		t.Fatalf("el mensaje sin fila no se queda en Scheduled: %v", h.mb.expunged)
	}
	h.directory.createErr = nil
	if res, err := h.svc.Schedule(context.Background(), sess, scheduledDraft(), h.clock.Now().Add(time.Hour), opts); err != nil || res.Replayed {
		t.Fatalf("la clave queda libre para reintentar: %+v %v", res, err)
	}
}

func TestProgramarUnBorradorLoRetira(t *testing.T) {
	h, sess := newScheduledHarness(t)
	h.mb.put("Drafts", 5, &fakeMessage{raw: []byte("borrador")})
	h.schedule(t, sess, sendOpts(5))
	if strings.Join(h.mb.expunged, ",") != "Drafts:5" {
		t.Fatalf("%v", h.mb.expunged)
	}
}

func TestElTrabajadorEnviaGuardaEnEnviadosYCierra(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	claim := h.claimOf(res.ID)
	h.clock.Advance(time.Hour)

	outcome := h.svc.deliverScheduled(context.Background(), claim)
	if outcome.Status != domain.ScheduledSent {
		t.Fatalf("%+v", outcome)
	}
	if len(h.sender.calls) != 1 {
		t.Fatalf("envios: %d", len(h.sender.calls))
	}
	call := h.sender.calls[0]
	if call.username != testUser || call.from != testUser || strings.Join(call.rcpts, ",") != "luis@x.pe,oculto@x.pe" ||
		!strings.HasPrefix(string(call.raw), "bcc=false;") {
		t.Fatalf("el Bcc no viaja en el cable: %+v %q", call, call.raw)
	}
	if len(h.composer.finalized) != 1 || !h.composer.finalized[0].Equal(h.clock.Now()) {
		t.Fatalf("la fecha del mensaje es la del envio: %v", h.composer.finalized)
	}
	last := h.mb.appended[len(h.mb.appended)-1]
	if last.folder != "Sent" || !strings.HasPrefix(string(last.raw), "final;bcc=true;") {
		t.Fatalf("copia en Enviados con Bcc: %+v", last)
	}
	if len(h.mb.messages["Scheduled"]) != 0 {
		t.Fatal("el mensaje sale de Scheduled")
	}
}

func TestElTrabajadorCancelaSiElMensajeYaNoEsta(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	claim := h.claimOf(res.ID)
	// El usuario lo movio a otra carpeta con su cliente de correo.
	if _, err := h.mb.Move(context.Background(), "Scheduled", []uint32{claim.UID}, "Archive"); err != nil {
		t.Fatal(err)
	}
	outcome := h.svc.deliverScheduled(context.Background(), claim)
	if outcome.Status != domain.ScheduledCanceled || len(h.sender.calls) != 0 {
		t.Fatalf("%+v envios=%d", outcome, len(h.sender.calls))
	}
}

func TestElTrabajadorLocalizaPorMessageIDSiCambioElUID(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	claim := h.claimOf(res.ID)
	claim.UIDValidity = 1
	if outcome := h.svc.deliverScheduled(context.Background(), claim); outcome.Status != domain.ScheduledSent || len(h.sender.calls) != 1 {
		t.Fatalf("%+v envios=%d", outcome, len(h.sender.calls))
	}
}

func TestUnFalloDeInfraestructuraSeReintentaSinDuplicar(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	claim := h.claimOf(res.ID)

	h.sender.err = fmt.Errorf("%w: postfix caido", domain.ErrUnavailable)
	outcome := h.svc.deliverScheduled(context.Background(), claim)
	if outcome.Status != domain.ScheduledFailed || !outcome.Retry {
		t.Fatalf("fallo de infraestructura: %+v", outcome)
	}
	if len(h.mb.messages["Scheduled"]) != 1 {
		t.Fatal("el mensaje sigue en Scheduled para el reintento")
	}
	h.sender.err = nil
	if outcome = h.svc.deliverScheduled(context.Background(), claim); outcome.Status != domain.ScheduledSent {
		t.Fatalf("reintento: %+v", outcome)
	}
	if len(h.sender.calls) != 2 {
		t.Fatalf("un intento fallido y uno bueno: %d", len(h.sender.calls))
	}
}

func TestUnRechazoNoSeReintenta(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	h.sender.err = &domain.RecipientRejectedError{Address: "luis@x.pe"}
	if outcome := h.svc.deliverScheduled(context.Background(), h.claimOf(res.ID)); outcome.Status != domain.ScheduledFailed || outcome.Retry ||
		!strings.Contains(outcome.Error, "luis@x.pe") {
		t.Fatalf("%+v", outcome)
	}
}

func TestElRemitenteSeVuelveAComprobarAlEnviar(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	h.composer.final.From = "ventas@empresa.pe"
	outcome := h.svc.deliverScheduled(context.Background(), h.claimOf(res.ID))
	if outcome.Status != domain.ScheduledFailed || outcome.Retry || len(h.sender.calls) != 0 {
		t.Fatalf("un remitente que el buzon ya no tiene: %+v", outcome)
	}
}

func TestSiElCierreSePierdeNoSeEnviaDosVeces(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	h.directory.claimQueue = []domain.ScheduledClaim{h.claimOf(res.ID)}
	h.directory.finishErr = errors.New("mail-directory caido")
	h.svc.processScheduledBatch(context.Background())
	if len(h.sender.calls) != 1 {
		t.Fatalf("primer intento: %d", len(h.sender.calls))
	}

	// El arriendo vence y otra replica la reclama: el registro de envios dice que ya salio.
	h.directory.finishErr = nil
	h.directory.claimQueue = []domain.ScheduledClaim{h.claimOf(res.ID)}
	h.svc.processScheduledBatch(context.Background())
	if len(h.sender.calls) != 1 {
		t.Fatalf("no se entrega de nuevo: %d", len(h.sender.calls))
	}
	if got := h.directory.finished[res.ID]; len(got) != 1 || got[0].Status != domain.ScheduledSent {
		t.Fatalf("cierre: %+v", got)
	}

	// Aunque el registro se pierda (Redis descarta claves), la copia en Enviados lo dice.
	h.ledger.records = map[string]domain.SendRecord{}
	h.directory.claimQueue = []domain.ScheduledClaim{h.claimOf(res.ID)}
	h.svc.processScheduledBatch(context.Background())
	if got := h.directory.finished[res.ID]; len(h.sender.calls) != 1 || len(got) != 2 || got[1].Status != domain.ScheduledSent {
		t.Fatalf("sin registro: envios=%d cierres=%+v", len(h.sender.calls), got)
	}
}

func TestUnEnvioInciertoNoSeRepite(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	claim := h.claimOf(res.ID)
	h.sender.err = domain.ErrDeliveryUncertain
	if outcome := h.svc.deliverScheduled(context.Background(), claim); outcome.Status != domain.ScheduledFailed || outcome.Retry {
		t.Fatalf("%+v", outcome)
	}
	h.sender.err = nil
	if outcome := h.svc.deliverScheduled(context.Background(), claim); outcome.Status != domain.ScheduledFailed || outcome.Retry {
		t.Fatalf("reintento: %+v", outcome)
	}
	if len(h.sender.calls) != 1 {
		t.Fatalf("el envio en duda llego una sola vez: %d", len(h.sender.calls))
	}
}

func TestUnaFilaEnCursoNoSeCierra(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	claim := h.claimOf(res.ID)
	key := sendKey(testUser, "scheduled\x00"+claim.ID)
	h.ledger.records[key] = domain.SendRecord{State: domain.SendPending, Fingerprint: "scheduled:" + claim.ID, Token: "otro"}
	if outcome := h.svc.deliverScheduled(context.Background(), claim); outcome.Status != "" || len(h.sender.calls) != 0 {
		t.Fatalf("otra replica la esta enviando: %+v", outcome)
	}
}

func TestElTrabajadorReclamaHastaVaciarYTerminaConElContexto(t *testing.T) {
	h, sess := newScheduledHarness(t)
	var claims []domain.ScheduledClaim
	for range 7 {
		res := h.schedule(t, sess, sendOpts(0))
		claims = append(claims, h.claimOf(res.ID))
	}
	h.directory.claimQueue = claims
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.svc.RunScheduledSends(ctx)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		h.directory.mu.Lock()
		n := len(h.directory.finished)
		h.directory.mu.Unlock()
		if n == 7 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cerradas %d de 7", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if h.directory.claimLease <= h.svc.cfg.SendTimeout {
		t.Fatalf("el arriendo cubre el envio entero: %v", h.directory.claimLease)
	}
}

func TestCancelarDevuelveElMensajeABorradores(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	uid := h.directory.scheduled[res.ID].UID
	if err := h.svc.CancelScheduled(context.Background(), sess, res.ID); err != nil {
		t.Fatal(err)
	}
	if h.directory.scheduled[res.ID].Status != domain.ScheduledCanceled {
		t.Fatal("la fila queda cancelada")
	}
	if strings.Join(h.mb.moved, ",") != fmt.Sprintf("Scheduled:%d->Drafts", uid) {
		t.Fatalf("%v", h.mb.moved)
	}
	if err := h.svc.CancelScheduled(context.Background(), sess, "00000000-0000-4000-8000-999999999999"); !errors.Is(err, domain.ErrScheduledNotFound) {
		t.Fatalf("fila ajena o inexistente: %v", err)
	}
	var verr *domain.ValidationError
	if err := h.svc.CancelScheduled(context.Background(), sess, "../x"); !errors.As(err, &verr) {
		t.Fatalf("id invalido: %v", err)
	}
	h.directory.cancelErr = domain.ErrScheduledNotPending
	if err := h.svc.CancelScheduled(context.Background(), sess, res.ID); !errors.Is(err, domain.ErrScheduledNotPending) {
		t.Fatalf("ya enviandose: %v", err)
	}
}

func TestReprogramar(t *testing.T) {
	h, sess := newScheduledHarness(t)
	res := h.schedule(t, sess, sendOpts(0))
	at := h.clock.Now().Add(3 * time.Hour)
	row, err := h.svc.Reschedule(context.Background(), sess, res.ID, at)
	if err != nil || !row.SendAt.Equal(at) {
		t.Fatalf("%+v %v", row, err)
	}
	var verr *domain.ValidationError
	if _, err := h.svc.Reschedule(context.Background(), sess, res.ID, h.clock.Now()); !errors.As(err, &verr) {
		t.Fatalf("hora pasada: %v", err)
	}
	if _, err := h.svc.Reschedule(context.Background(), sess, "00000000-0000-4000-8000-999999999999", at); !errors.Is(err, domain.ErrScheduledNotFound) {
		t.Fatalf("inexistente: %v", err)
	}
}
