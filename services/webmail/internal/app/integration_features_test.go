//go:build integration

package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Carpetas propias, acciones en lote, vaciado, original y busqueda avanzada contra el IMAP en
// memoria con el adaptador real.
func TestIntegracionCarpetasLoteYBusqueda(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	svc := env.svc
	seed(t, env.store)
	sess := loginIntegration(t, svc)

	// Busqueda avanzada: fechas de la cabecera Date, remitente, asunto, no leidos y adjuntos.
	search := func(f domain.SearchFilter) []uint32 {
		t.Helper()
		q, _ := domain.NewListQuery(1, 50, "")
		q, err := q.WithFilter(f)
		if err != nil {
			t.Fatal(err)
		}
		page, err := svc.ListMessages(ctx, sess, "INBOX", q)
		if err != nil {
			t.Fatal(err)
		}
		var uids []uint32
		for _, e := range page.Items {
			uids = append(uids, e.UID)
		}
		if page.Total != len(uids) {
			t.Fatalf("total %d con %d filas", page.Total, len(uids))
		}
		return uids
	}
	day := func(s string) time.Time { d, _ := domain.ParseSearchDate("since", s); return d }
	checks := []struct {
		name   string
		filter domain.SearchFilter
		want   string
	}{
		{"desde", domain.SearchFilter{Since: day("2026-09-11")}, "[3 2]"},
		{"antes", domain.SearchFilter{Before: day("2026-09-11")}, "[1]"},
		{"remitente", domain.SearchFilter{From: "proveedor"}, "[3]"},
		{"destinatario", domain.SearchFilter{To: "ana@empresa"}, "[3 2 1]"},
		{"asunto", domain.SearchFilter{Subject: "novedades"}, "[2]"},
		{"no leidos", domain.SearchFilter{Unread: true}, "[3 2 1]"},
		{"destacados", domain.SearchFilter{Flagged: true}, "[]"},
		{"con adjuntos", domain.SearchFilter{HasAttachments: true}, "[2]"},
		{"combinado", domain.SearchFilter{HasAttachments: true, From: "proveedor"}, "[]"},
	}
	for _, c := range checks {
		if got := fmt.Sprint(search(c.filter)); got != c.want {
			t.Errorf("%s: %s, quiero %s", c.name, got, c.want)
		}
	}

	// El original sale byte a byte y acotado por el tope de descarga.
	var raw bytes.Buffer
	err := svc.StreamRaw(ctx, sess, "INBOX", 1, func(m domain.StoredMessage, body io.Reader) error {
		if m.UIDValidity == 0 || m.MessageID != "msg1@x.test" {
			t.Errorf("referencia: %+v", m)
		}
		_, err := io.Copy(&raw, body)
		return err
	})
	if err != nil || !strings.HasPrefix(raw.String(), "From: Luis <luis@x.test>\r\n") || !strings.HasSuffix(raw.String(), "Primer mensaje\r\n") {
		t.Fatalf("original: %q %v", raw.String(), err)
	}

	// Carpetas propias: crear (suscrita), renombrar y borrar; las del sistema no se tocan.
	if f, err := svc.CreateFolder(ctx, sess, "Proyectos"); err != nil || f.Name != "Proyectos" {
		t.Fatalf("crear: %+v %v", f, err)
	}
	if _, err := svc.CreateFolder(ctx, sess, "Trash"); !errors.Is(err, domain.ErrFolderExists) {
		t.Fatalf("crear existente: %v", err)
	}
	if _, err := svc.RenameFolder(ctx, sess, "Sent", "Enviados"); !errors.Is(err, domain.ErrFolderProtected) {
		t.Fatalf("renombrar Enviados: %v", err)
	}
	if err := svc.DeleteFolder(ctx, sess, "Archive"); !errors.Is(err, domain.ErrFolderProtected) {
		t.Fatalf("borrar Archivo: %v", err)
	}
	if _, err := svc.RenameFolder(ctx, sess, "Proyectos", "Clientes"); err != nil {
		t.Fatalf("renombrar: %v", err)
	}
	if !hasFolder(t, svc, sess, "Clientes") || hasFolder(t, svc, sess, "Proyectos") {
		t.Fatal("el renombrado se ve en el listado")
	}

	// Lote: marcar, mover a la carpeta propia (con un UID que no existe), a la papelera y borrar.
	flags, _ := domain.NewBatch("INBOX", []uint32{1, 2}, "flags", []string{`\Seen`, `\Flagged`}, nil, "")
	if res, err := svc.Batch(ctx, sess, "INBOX", flags); err != nil || res.Affected != 2 {
		t.Fatalf("marcar: %+v %v", res, err)
	}
	if got := fmt.Sprint(search(domain.SearchFilter{Flagged: true})); got != "[2 1]" {
		t.Fatalf("destacados tras el lote: %s", got)
	}
	move, _ := domain.NewBatch("INBOX", []uint32{1, 2, 99}, "move", nil, nil, "Clientes")
	if res, err := svc.Batch(ctx, sess, "INBOX", move); err != nil || res.Affected != 2 {
		t.Fatalf("mover: %+v %v", res, err)
	}
	moved := listAll(t, svc, sess, "Clientes")
	if len(moved) != 2 || len(listAll(t, svc, sess, "INBOX")) != 1 {
		t.Fatalf("mover: %+v", moved)
	}
	var uids []uint32
	for _, e := range moved {
		uids = append(uids, e.UID)
	}
	del, _ := domain.NewBatch("Clientes", uids, "delete", nil, nil, "")
	if res, err := svc.Batch(ctx, sess, "Clientes", del); err != nil || res.Affected != 2 || res.Permanent {
		t.Fatalf("a la papelera: %+v %v", res, err)
	}
	trash := listAll(t, svc, sess, "Trash")
	if len(trash) != 2 {
		t.Fatalf("papelera: %+v", trash)
	}
	fromTrash, _ := domain.NewBatch("Trash", []uint32{trash[0].UID}, "delete", nil, nil, "")
	if res, err := svc.Batch(ctx, sess, "Trash", fromTrash); err != nil || res.Affected != 1 || !res.Permanent {
		t.Fatalf("definitivo: %+v %v", res, err)
	}

	// Vaciar: solo papelera y spam.
	if _, err := svc.EmptyFolder(ctx, sess, "INBOX"); !errors.Is(err, domain.ErrFolderNotEmptiable) {
		t.Fatalf("vaciar INBOX: %v", err)
	}
	if n, err := svc.EmptyFolder(ctx, sess, "Trash"); err != nil || n != 1 || len(listAll(t, svc, sess, "Trash")) != 0 {
		t.Fatalf("vaciar papelera: %d %v", n, err)
	}

	// Una carpeta sin subcarpetas se borra con sus mensajes.
	if err := svc.DeleteFolder(ctx, sess, "Clientes"); err != nil || hasFolder(t, svc, sess, "Clientes") {
		t.Fatalf("borrar carpeta: %v", err)
	}
}

// Envio programado de punta a punta: el mensaje espera en Scheduled, el trabajador lo reclama, sale
// por SMTP sin Bcc, queda en Enviados con Bcc y se retira de Scheduled; otro cancelado vuelve a
// Borradores.
func TestIntegracionEnvioProgramado(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	svc := env.svc
	sess := loginIntegration(t, svc)

	draft := domain.Draft{
		To: []domain.Address{{Email: "luis@x.test"}}, Bcc: []domain.Address{{Email: "oculto@x.test"}},
		Subject: "Programado", Text: "sale luego",
	}
	res, err := svc.Schedule(ctx, sess, draft, time.Now().Add(time.Hour), domain.SendOptions{IdempotencyKey: "integracion-programado-0001"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := listAll(t, svc, sess, domain.ScheduledFolderName)
	if len(waiting) != 1 || !hasFlag(waiting[0].Flags, domain.FlagDraft) || len(env.smtp.received()) != 0 {
		t.Fatalf("en Scheduled sin salir: %+v", waiting)
	}
	row := env.scheduled.row(res.ID)
	if row.UID != waiting[0].UID || row.UIDValidity == 0 || row.Folder != domain.ScheduledFolderName {
		t.Fatalf("referencia IMAP de la fila: %+v", row)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.RunScheduledSends(runCtx); close(done) }()
	env.scheduled.waitFinished(t, res.ID)
	stop()
	<-done

	if got := env.scheduled.row(res.ID).Status; got != domain.ScheduledSent {
		t.Fatalf("estado: %s", got)
	}
	delivered := env.smtp.received()
	if len(delivered) != 1 || strings.Join(delivered[0].rcpts, ",") != "luis@x.test,oculto@x.test" || delivered[0].authUser != mailbox {
		t.Fatalf("entrega: %+v", delivered)
	}
	if wire := string(delivered[0].data); strings.Contains(wire, "oculto@x.test") || !strings.Contains(wire, "Subject: Programado") {
		t.Fatalf("el Bcc no viaja:\n%s", wire)
	}
	sent := listAll(t, svc, sess, "Sent")
	if len(sent) != 1 || sent[0].Subject != "Programado" || len(listAll(t, svc, sess, domain.ScheduledFolderName)) != 0 {
		t.Fatalf("Enviados %+v", sent)
	}
	copyMsg, err := svc.ReadMessage(ctx, sess, "Sent", sent[0].UID, true, false)
	if err != nil || len(copyMsg.Bcc) != 1 || copyMsg.MessageID != row.MessageID {
		t.Fatalf("la copia conserva el Bcc y el Message-ID: %+v %v", copyMsg, err)
	}

	// Cancelar devuelve el mensaje a Borradores.
	other, err := svc.Schedule(ctx, sess, draft, time.Now().Add(time.Hour), domain.SendOptions{IdempotencyKey: "integracion-programado-0002"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelScheduled(ctx, sess, other.ID); err != nil {
		t.Fatal(err)
	}
	drafts := listAll(t, svc, sess, "Drafts")
	if len(drafts) != 1 || drafts[0].Subject != "Programado" || len(listAll(t, svc, sess, domain.ScheduledFolderName)) != 0 {
		t.Fatalf("Borradores: %+v", drafts)
	}
	if env.scheduled.row(other.ID).Status != domain.ScheduledCanceled || len(env.smtp.received()) != 1 {
		t.Fatal("la fila cancelada no sale")
	}
}

func loginIntegration(t *testing.T, svc interface {
	Login(context.Context, string, string, string, string) (app.LoginResult, error)
	Authenticate(context.Context, string) (domain.Session, error)
}) domain.Session {
	t.Helper()
	login, err := svc.Login(context.Background(), mailbox, password, "203.0.113.7", "")
	if err != nil {
		t.Fatal(err)
	}
	token := login.Token
	sess, err := svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func hasFolder(t *testing.T, svc interface {
	Folders(context.Context, domain.Session) ([]domain.Folder, error)
}, sess domain.Session, name string) bool {
	t.Helper()
	folders, err := svc.Folders(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := domain.FindFolder(folders, name)
	return ok
}

// memScheduled hace de indice de envios programados de mail-directory: reclamar pasa las pendientes
// a sending sin mirar la hora (la prueba no espera) y cerrar las deja en su estado final.
type memScheduled struct {
	mu   sync.Mutex
	rows map[string]*domain.ScheduledSend
	user map[string]string
	seq  int
}

func newMemScheduled() *memScheduled {
	return &memScheduled{rows: map[string]*domain.ScheduledSend{}, user: map[string]string{}}
}

func (m *memScheduled) row(id string) domain.ScheduledSend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.rows[id]
}

func (m *memScheduled) waitFinished(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.row(id).Status; st != domain.ScheduledPending && st != domain.ScheduledSending {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("la fila %s no se cerro", id)
}

func (m *memScheduled) CreateScheduled(_ context.Context, in domain.NewScheduledSend) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", m.seq)
	m.rows[id] = &domain.ScheduledSend{ID: id, SendAt: in.SendAt, Subject: in.Subject, Recipients: in.Recipients, Status: domain.ScheduledPending,
		MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity, UID: in.UID, CreatedAt: time.Now()}
	m.user[id] = in.Username
	return id, nil
}

func (m *memScheduled) ListScheduled(_ context.Context, username string) ([]domain.ScheduledSend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ScheduledSend
	for id, r := range m.rows {
		if m.user[id] == username && r.Status != domain.ScheduledCanceled && r.Status != domain.ScheduledSent {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *memScheduled) RescheduleScheduled(_ context.Context, _, id string, at time.Time) (domain.ScheduledSend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return domain.ScheduledSend{}, domain.ErrScheduledNotFound
	}
	r.SendAt = at
	return *r, nil
}

func (m *memScheduled) CancelScheduled(_ context.Context, _, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return domain.ErrScheduledNotFound
	}
	if r.Status != domain.ScheduledPending && r.Status != domain.ScheduledCanceled {
		return domain.ErrScheduledNotPending
	}
	r.Status = domain.ScheduledCanceled
	return nil
}

func (m *memScheduled) ClaimScheduled(_ context.Context, limit int, _ time.Duration) ([]domain.ScheduledClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ScheduledClaim
	for id, r := range m.rows {
		if len(out) == limit {
			break
		}
		if r.Status == domain.ScheduledPending {
			r.Status = domain.ScheduledSending
			out = append(out, domain.ScheduledClaim{ID: id, Username: m.user[id], MessageID: r.MessageID, Folder: r.Folder,
				UIDValidity: r.UIDValidity, UID: r.UID, SendAt: r.SendAt})
		}
	}
	return out, nil
}

func (m *memScheduled) FinishScheduled(_ context.Context, id string, outcome domain.ScheduledOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok || r.Status != domain.ScheduledSending {
		return domain.ErrScheduledNotClaimed
	}
	r.Status = outcome.Status
	return nil
}

func (d staticDirectory) Signature(context.Context, string) (domain.Signature, error) {
	return domain.Signature{}, nil
}

func (d staticDirectory) SetSignature(context.Context, string, domain.SignatureInput) (domain.Signature, error) {
	return domain.Signature{}, nil
}

func (d staticDirectory) Filters(context.Context, string) (domain.MailFilters, error) {
	return domain.MailFilters{}, nil
}

func (d staticDirectory) SetFilters(context.Context, string, domain.MailFiltersInput) (domain.MailFilters, error) {
	return domain.MailFilters{}, nil
}

func (d staticDirectory) SetPassword(context.Context, string, string) error { return nil }

// noSecurity es una verificacion en dos pasos que no responde: los buzones de estas pruebas no la
// tienen activa.
type noSecurity struct{}

func (noSecurity) CreateChallenge(context.Context, string, domain.MFAChallenge, time.Duration) error {
	return domain.ErrUnavailable
}
func (noSecurity) GetChallenge(context.Context, string) (domain.MFAChallenge, error) {
	return domain.MFAChallenge{}, domain.ErrUnavailable
}
func (noSecurity) CountAttempt(context.Context, string) (int, error) { return 0, domain.ErrUnavailable }
func (noSecurity) DeleteChallenge(context.Context, string) error     { return domain.ErrUnavailable }
func (noSecurity) SaveSetup(context.Context, string, string, time.Duration) error {
	return domain.ErrUnavailable
}
func (noSecurity) SetupHash(context.Context, string) (string, error) {
	return "", domain.ErrUnavailable
}
func (noSecurity) DeleteSetup(context.Context, string) error { return domain.ErrUnavailable }
func (noSecurity) MFAStatus(context.Context, string) (domain.MFAStatus, error) {
	return domain.MFAStatus{}, domain.ErrUnavailable
}
func (noSecurity) ActivateMFA(context.Context, string, string, string) ([]string, error) {
	return nil, domain.ErrUnavailable
}
func (noSecurity) VerifyMFA(context.Context, string, string) (domain.MFAVerification, error) {
	return domain.MFAVerification{}, domain.ErrUnavailable
}
func (noSecurity) RegenerateRecoveryCodes(context.Context, string, string) ([]string, error) {
	return nil, domain.ErrUnavailable
}
func (noSecurity) DisableMFA(context.Context, string, string) error { return domain.ErrUnavailable }
func (noSecurity) AppPasswords(context.Context, string) (domain.AppPasswordList, error) {
	return domain.AppPasswordList{}, domain.ErrUnavailable
}
func (noSecurity) CreateAppPassword(context.Context, string, domain.AppPasswordInput) (domain.CreatedAppPassword, error) {
	return domain.CreatedAppPassword{}, domain.ErrUnavailable
}
func (noSecurity) DeleteAppPassword(context.Context, string, string) error {
	return domain.ErrUnavailable
}
func (noSecurity) NewSecret() (string, error)            { return "", domain.ErrUnavailable }
func (noSecurity) ProvisioningURI(string, string) string { return "" }

// noDAV es un mail-dav que no responde: estas pruebas no usan la libreta ni el calendario.
type noDAV struct{}

func (noDAV) ListContacts(context.Context, domain.MailboxRef, domain.ContactQuery) (domain.ContactPage, error) {
	return domain.ContactPage{}, domain.ErrUnavailable
}
func (noDAV) Contact(context.Context, domain.MailboxRef, string) (domain.Contact, error) {
	return domain.Contact{}, domain.ErrUnavailable
}
func (noDAV) CreateContact(context.Context, domain.MailboxRef, domain.ContactInput) (domain.Contact, error) {
	return domain.Contact{}, domain.ErrUnavailable
}
func (noDAV) UpdateContact(context.Context, domain.MailboxRef, string, domain.ContactInput, string) (domain.Contact, error) {
	return domain.Contact{}, domain.ErrUnavailable
}
func (noDAV) DeleteContact(context.Context, domain.MailboxRef, string) error {
	return domain.ErrUnavailable
}
func (noDAV) ExportContacts(context.Context, domain.MailboxRef) (io.ReadCloser, error) {
	return nil, domain.ErrUnavailable
}
func (noDAV) ImportContacts(context.Context, domain.MailboxRef, string, []byte) (domain.ImportResult, error) {
	return domain.ImportResult{}, domain.ErrUnavailable
}
func (noDAV) Limits(context.Context) (map[string]int64, error) { return nil, domain.ErrUnavailable }
func (noDAV) Occurrences(context.Context, domain.MailboxRef, domain.EventWindow) ([]domain.Occurrence, error) {
	return nil, domain.ErrUnavailable
}
func (noDAV) Event(context.Context, domain.MailboxRef, string) (domain.Event, error) {
	return domain.Event{}, domain.ErrUnavailable
}
func (noDAV) CreateEvent(context.Context, domain.MailboxRef, domain.EventInput) (domain.Event, error) {
	return domain.Event{}, domain.ErrUnavailable
}
func (noDAV) UpdateEvent(context.Context, domain.MailboxRef, string, domain.EventInput, string) (domain.Event, error) {
	return domain.Event{}, domain.ErrUnavailable
}
func (noDAV) DeleteEvent(context.Context, domain.MailboxRef, string) error {
	return domain.ErrUnavailable
}
