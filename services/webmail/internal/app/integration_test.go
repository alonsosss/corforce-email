//go:build integration

// Prueba de integracion del webmail con adaptadores reales contra un servidor IMAP en
// memoria (imapmemserver de go-imap, con TLS implicito) y un servidor SMTP en proceso
// con STARTTLS que emula lo que hacen Dovecot y Postfix en la celda: el inicio como
// usuario*maestro autentica al buzon real y el remitente solo puede ser uno propio.
//
//	go test -tags integration ./services/webmail/internal/app/ -run Integration -count=1
package app_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"go.uber.org/zap"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/htmlsafe"
	handler "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/http"
	imapadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/imap"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/rfc5322"
	smtpadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/smtp"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/unsubscribe"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	mailbox    = "ana@empresa.test"
	password   = "contrasena-del-buzon"
	masterUser = "webmail@platform.local"
	masterPass = "maestra-de-prueba-de-treinta-y-dos"
)

func TestIntegracionWebmailContraIMAPYSMTP(t *testing.T) {
	ctx := context.Background()
	env := newIntegration(t)
	svc, store, smtpSrv := env.svc, env.store, env.smtp

	seed(t, store)

	login, err := svc.Login(ctx, mailbox, password, "203.0.113.7", "")
	if err != nil {
		t.Fatal(err)
	}
	token := login.Token
	sess, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	// Carpetas: los papeles especiales y los contadores.
	folders, err := svc.Folders(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[domain.FolderRole]domain.Folder{}
	for _, f := range folders {
		roles[f.Role] = f
	}
	for _, r := range []domain.FolderRole{domain.RoleInbox, domain.RoleSent, domain.RoleDrafts, domain.RoleTrash} {
		if _, ok := roles[r]; !ok {
			t.Fatalf("falta la carpeta %s: %+v", r, folders)
		}
	}
	if inbox := roles[domain.RoleInbox]; inbox.Total != 3 || inbox.Unread != 3 || folders[0].Role != domain.RoleInbox {
		t.Fatalf("INBOX: %+v (primera: %+v)", inbox, folders[0])
	}

	// Listado paginado, del mas reciente al mas antiguo, y busqueda.
	q, _ := domain.NewListQuery(1, 2, "")
	page, err := svc.ListMessages(ctx, sess, "INBOX", q)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 2 || page.Items[0].UID != 3 || page.Items[1].UID != 2 {
		t.Fatalf("pagina: total=%d items=%+v", page.Total, page.Items)
	}
	if !page.Items[1].HasAttachments || page.Items[1].Subject != "Novedades" || page.Items[1].From[0].Email != "news@tercero.test" {
		t.Fatalf("sobre con adjunto: %+v", page.Items[1])
	}
	q, _ = domain.NewListQuery(1, 10, "mensual")
	found, err := svc.ListMessages(ctx, sess, "INBOX", q)
	if err != nil || found.Total != 1 || found.Items[0].Subject != "Factura mensual" {
		t.Fatalf("busqueda: %+v %v", found, err)
	}

	// Lectura con saneado, cid resuelto, remotas bloqueadas y marca de leido.
	msg, err := svc.ReadMessage(ctx, sess, "INBOX", 2, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script", "alert", "onclick", "tracker.test", "javascript"} {
		if strings.Contains(strings.ToLower(msg.HTML), bad) {
			t.Fatalf("HTML sin sanear (%s): %s", bad, msg.HTML)
		}
	}
	logoURL := handler.PartURL("INBOX", 2, "1.2")
	if !strings.Contains(msg.HTML, `src="`+logoURL+`"`) || !msg.RemoteImages.Present || !msg.RemoteImages.Blocked {
		t.Fatalf("imagenes: %+v html=%s", msg.RemoteImages, msg.HTML)
	}
	if strings.TrimSpace(msg.Text) != "Hola Ana" || msg.MessageID != "msg2@tercero.test" {
		t.Fatalf("texto=%q message_id=%q", msg.Text, msg.MessageID)
	}
	var pdf, logo *domain.Part
	for i := range msg.Attachments {
		switch msg.Attachments[i].Filename {
		case "informe.pdf":
			pdf = &msg.Attachments[i]
		case "logo.png":
			logo = &msg.Attachments[i]
		}
	}
	if pdf == nil || pdf.ID != "2" || logo == nil || !logo.Inline || logo.ContentID != "logo@tercero.test" {
		t.Fatalf("partes: %+v", msg.Attachments)
	}
	if !hasFlag(msg.Flags, domain.FlagSeen) {
		t.Fatalf("leer marca como leido: %v", msg.Flags)
	}
	peeked, err := svc.ReadMessage(ctx, sess, "INBOX", 1, true, false)
	if err != nil || hasFlag(peeked.Flags, domain.FlagSeen) {
		t.Fatalf("peek no marca como leido: %v %v", peeked.Flags, err)
	}

	// Descarga del adjunto decodificado.
	var got bytes.Buffer
	err = svc.StreamPart(ctx, sess, "INBOX", 2, "2", func(p domain.Part, body io.Reader) error {
		_, err := io.Copy(&got, body)
		return err
	})
	if err != nil || got.String() != "%PDF-1.4\n" {
		t.Fatalf("adjunto: %q %v", got.String(), err)
	}

	// Marcar, mover y borrar.
	change, _ := domain.NewFlagChange([]string{`\Flagged`}, nil)
	if err := svc.ChangeFlags(ctx, sess, "INBOX", 1, change); err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangeFlags(ctx, sess, "INBOX", 99, change); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Fatalf("UID inexistente: %v", err)
	}
	if err := svc.Move(ctx, sess, "INBOX", 1, "Archive"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Move(ctx, sess, "INBOX", 2, "NoExiste"); !errors.Is(err, domain.ErrFolderNotFound) {
		t.Fatalf("destino inexistente: %v", err)
	}
	archived := listAll(t, svc, sess, "Archive")
	if len(archived) != 1 || !hasFlag(archived[0].Flags, domain.FlagFlagged) {
		t.Fatalf("archivado: %+v", archived)
	}
	if permanent, err := svc.Delete(ctx, sess, "INBOX", 3); err != nil || permanent {
		t.Fatalf("a la papelera: %v %v", permanent, err)
	}
	trash := listAll(t, svc, sess, "Trash")
	if len(trash) != 1 {
		t.Fatalf("papelera: %+v", trash)
	}
	if permanent, err := svc.Delete(ctx, sess, "Trash", trash[0].UID); err != nil || !permanent {
		t.Fatalf("borrado definitivo: %v %v", permanent, err)
	}
	if left := listAll(t, svc, sess, "Trash"); len(left) != 0 {
		t.Fatalf("la papelera debe quedar vacia: %+v", left)
	}

	// Envio de una respuesta con Bcc y adjunto: sale por SMTP autenticado como el buzon y
	// la copia (con Bcc) queda en Enviados.
	res, err := svc.Send(ctx, sess, domain.Draft{
		To:          []domain.Address{{Name: "Luis", Email: "luis@x.test"}},
		Bcc:         []domain.Address{{Email: "oculto@x.test"}},
		Subject:     "Re: Novedades",
		HTML:        `<p>Gracias</p><script>alert(1)</script>`,
		Attachments: []domain.Attachment{{Filename: "nota.txt", ContentType: "text/plain", Data: []byte("hola")}},
		InReplyTo:   &domain.ReplyTarget{Folder: "INBOX", UID: 2},
	}, domain.SendOptions{IdempotencyKey: "integracion-respuesta-0001"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SavedToSent {
		t.Fatal("la copia debe guardarse en Enviados")
	}
	delivered := smtpSrv.received()
	if len(delivered) != 1 {
		t.Fatalf("mensajes entregados: %d", len(delivered))
	}
	d := delivered[0]
	if d.authUser != mailbox || d.from != mailbox || strings.Join(d.rcpts, ",") != "luis@x.test,oculto@x.test" {
		t.Fatalf("sobre SMTP: %+v", d)
	}
	wire := string(d.data)
	if strings.Contains(wire, "oculto@x.test") || strings.Contains(wire, "<script") ||
		!strings.Contains(wire, "In-Reply-To: <msg2@tercero.test>") || !strings.Contains(wire, "nota.txt") {
		t.Fatalf("mensaje entregado:\n%s", wire)
	}
	sent := listAll(t, svc, sess, "Sent")
	if len(sent) != 1 || sent[0].Subject != "Re: Novedades" || !hasFlag(sent[0].Flags, domain.FlagSeen) {
		t.Fatalf("Enviados: %+v", sent)
	}
	copyMsg, err := svc.ReadMessage(ctx, sess, "Sent", sent[0].UID, true, false)
	if err != nil || len(copyMsg.Bcc) != 1 || copyMsg.Bcc[0].Email != "oculto@x.test" {
		t.Fatalf("la copia conserva el Bcc: %+v %v", copyMsg, err)
	}
	original := listAll(t, svc, sess, "INBOX")
	if len(original) != 1 || !hasFlag(original[0].Flags, domain.FlagAnswered) {
		t.Fatalf("el original queda respondido: %+v", original)
	}

	// Un remitente que el directorio no le da al buzon no llega a Postfix. Uno que el
	// directorio le da pero Postfix rechaza (el SMTP de prueba solo acepta el propio buzon)
	// tampoco sale: Postfix sigue siendo la ultima palabra.
	for i, from := range []string{"director@empresa.test", "ventas@empresa.test"} {
		_, err = svc.Send(ctx, sess, domain.Draft{From: domain.Address{Email: from}, To: []domain.Address{{Email: "luis@x.test"}}, Text: "x"},
			domain.SendOptions{IdempotencyKey: fmt.Sprintf("integracion-remitente-%04d", i)})
		if !errors.Is(err, domain.ErrSenderNotAllowed) {
			t.Fatalf("suplantacion con %s: %v", from, err)
		}
	}
	if len(smtpSrv.received()) != 1 || len(listAll(t, svc, sess, "Sent")) != 1 {
		t.Fatal("un envio rechazado no se entrega ni se guarda")
	}

	// Borradores: guardar y reemplazar deja uno solo.
	first, err := svc.SaveDraft(ctx, sess, domain.Draft{Subject: "Borrador", Text: "v1"}, 0)
	if err != nil || first == 0 {
		t.Fatalf("borrador: %d %v", first, err)
	}
	second, err := svc.SaveDraft(ctx, sess, domain.Draft{Subject: "Borrador", Text: "v2"}, first)
	if err != nil || second == first {
		t.Fatalf("reemplazo: %d %v", second, err)
	}
	drafts := listAll(t, svc, sess, "Drafts")
	if len(drafts) != 1 || drafts[0].UID != second || !hasFlag(drafts[0].Flags, domain.FlagDraft) {
		t.Fatalf("borradores: %+v", drafts)
	}

	// Un borrador con adjunto se envia tomando el adjunto del propio borrador en el servidor
	// y se retira en la misma operacion; el reintento con la misma clave no entrega nada.
	anexo := []byte("contenido del anexo")
	withFile, err := svc.SaveDraft(ctx, sess, domain.Draft{
		To: []domain.Address{{Email: "luis@x.test"}}, Subject: "Con anexo", Text: "v3",
		Attachments: []domain.Attachment{{Filename: "anexo.txt", ContentType: "text/plain", Data: anexo}},
	}, second)
	if err != nil || withFile == 0 {
		t.Fatalf("borrador con adjunto: %d %v", withFile, err)
	}
	saved, err := svc.ReadMessage(ctx, sess, "Drafts", withFile, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var anexoPart string
	for _, p := range saved.Attachments {
		if p.Filename == "anexo.txt" {
			anexoPart = p.ID
		}
	}
	if anexoPart == "" {
		t.Fatalf("el borrador guarda el adjunto: %+v", saved.Attachments)
	}
	draftSend := domain.Draft{
		To: []domain.Address{{Email: "luis@x.test"}}, Subject: "Con anexo", Text: "v3",
		Source: &domain.PartSource{Folder: "Drafts", UID: withFile, Parts: []string{anexoPart}},
	}
	opts := domain.SendOptions{IdempotencyKey: "integracion-borrador-0001", ReplaceUID: withFile}
	res, err = svc.Send(ctx, sess, draftSend, opts)
	if err != nil || !res.SavedToSent || !res.DraftRemoved || res.Replayed {
		t.Fatalf("envio del borrador: %+v %v", res, err)
	}
	if left := listAll(t, svc, sess, "Drafts"); len(left) != 0 {
		t.Fatalf("el borrador enviado se retira: %+v", left)
	}
	delivered = smtpSrv.received()
	if len(delivered) != 2 || !strings.Contains(string(delivered[1].data), "anexo.txt") ||
		!strings.Contains(string(delivered[1].data), base64.StdEncoding.EncodeToString(anexo)) {
		t.Fatalf("el adjunto del borrador sale con el mensaje:\n%s", delivered[len(delivered)-1].data)
	}
	res, err = svc.Send(ctx, sess, draftSend, opts)
	if err != nil || !res.Replayed || !res.DraftRemoved || len(smtpSrv.received()) != 2 {
		t.Fatalf("reintento: %+v %v", res, err)
	}

	// Reenvio con el adjunto del original tomado del servidor, sin pasar por el navegador.
	_, err = svc.Send(ctx, sess, domain.Draft{
		To: []domain.Address{{Email: "luis@x.test"}}, Subject: "Fwd: Novedades", Text: "reenvio",
		Source: &domain.PartSource{Folder: "INBOX", UID: 2, Parts: []string{"2"}},
	}, domain.SendOptions{IdempotencyKey: "integracion-reenvio-0001"})
	if err != nil {
		t.Fatal(err)
	}
	delivered = smtpSrv.received()
	if len(delivered) != 3 || !strings.Contains(string(delivered[2].data), "informe.pdf") ||
		!strings.Contains(string(delivered[2].data), "JVBERi0xLjQK") {
		t.Fatalf("reenvio:\n%s", delivered[len(delivered)-1].data)
	}

	// Si la conexion se corta tras el punto final de DATA el envio queda en duda y la misma
	// clave no lo repite.
	smtpSrv.setDropAfterData(true)
	corte := domain.Draft{To: []domain.Address{{Email: "luis@x.test"}}, Subject: "Corte", Text: "x"}
	uncertain := domain.SendOptions{IdempotencyKey: "integracion-incierto-0001"}
	if _, err := svc.Send(ctx, sess, corte, uncertain); !errors.Is(err, domain.ErrDeliveryUncertain) {
		t.Fatalf("corte tras DATA: %v", err)
	}
	smtpSrv.setDropAfterData(false)
	if _, err := svc.Send(ctx, sess, corte, uncertain); !errors.Is(err, domain.ErrDeliveryUncertain) {
		t.Fatalf("reintento de un envio en duda: %v", err)
	}
	if got := len(smtpSrv.received()); got != 4 {
		t.Fatalf("el envio en duda llego una sola vez al servidor: %d", got)
	}
}

// integration es el webmail con sus adaptadores reales contra IMAP y SMTP en proceso.
type integration struct {
	svc       *app.Service
	store     *imapadapter.Store
	smtp      *smtpServer
	scheduled *memScheduled
	reminders *memReminders
}

func newIntegration(t *testing.T) *integration {
	t.Helper()
	tlsServer, tlsClient := testTLS(t)
	imapAddr := startIMAP(t, tlsServer)
	smtpSrv := startSMTP(t, tlsServer)

	store, err := imapadapter.NewStore(imapadapter.Config{
		Addr: imapAddr, TLSMode: imapadapter.TLSImplicit, TLSConfig: tlsClient,
		MasterUser: masterUser, MasterPassword: masterPass,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	sender, err := smtpadapter.NewSender(smtpadapter.Config{
		Addr: smtpSrv.addr, TLSMode: smtpadapter.TLSStartTLS, TLSConfig: tlsClient,
		MasterUser: masterUser, MasterPassword: masterPass, HeloName: "webmail.test",
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	scheduled := newMemScheduled()
	reminders := newMemReminders()
	dir := staticDirectory{ids: []string{"ventas@empresa.test"}}
	svc, err := app.New(app.Deps{
		Auth: staticAuth{}, Sessions: newMemSessions(), Mail: store, Sender: sender,
		Directory: dir, Vacations: dir, AddressBook: dir, Signatures: dir, Filters: dir, Passwords: dir,
		Scheduled: scheduled, Contacts: noDAV{}, Calendar: noDAV{}, Ledger: newMemLedger(),
		Composer: rfc5322.New(), Sanitizer: htmlsafe.New(), PartURL: handler.PartURL, Logger: zap.NewNop(),
		Unsubscriber: unsubscribe.New(time.Second),
		Reminders:    reminders, QuickReplies: reminders,
		MFAChallenges: noSecurity{}, Security: noSecurity{}, TOTP: noSecurity{},
		Config: app.Config{
			CellCode:         "pe-01",
			Sessions:         domain.SessionPolicy{Idle: 30 * time.Minute, Max: 12 * time.Hour},
			Limits:           domain.Limits{MaxRecipients: 10, MaxMessageBytes: 1 << 20},
			MaxBodyPartBytes: 64 << 10, MaxAttachmentBytes: 1 << 20,
			SendTimeout: time.Minute, MaxScheduledDays: 30, ScheduledPollInterval: 50 * time.Millisecond,
			ScheduledBatch: 5, MaxImportBytes: 1 << 20,
			MaxReminderDays: 30, ReminderPollInterval: 50 * time.Millisecond, ReminderBatch: 5,
			MFAChallengeTTL: 5 * time.Minute, MFAMaxAttempts: 5, MFASetupTTL: 10 * time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &integration{svc: svc, store: store, smtp: smtpSrv, scheduled: scheduled, reminders: reminders}
}

func listAll(t *testing.T, svc *app.Service, sess domain.Session, folder string) []domain.Envelope {
	t.Helper()
	q, _ := domain.NewListQuery(1, domain.MaxPerPage, "")
	page, err := svc.ListMessages(context.Background(), sess, folder, q)
	if err != nil {
		t.Fatalf("listar %s: %v", folder, err)
	}
	return page.Items
}

func hasFlag(flags []domain.Flag, want domain.Flag) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// seed deja tres mensajes en INBOX (UID 1, 2 y 3) con el propio adaptador.
func seed(t *testing.T, store *imapadapter.Store) {
	t.Helper()
	mb, err := store.Open(context.Background(), mailbox)
	if err != nil {
		t.Fatal(err)
	}
	defer mb.Close()
	msgs := []string{
		"From: Luis <luis@x.test>\r\nTo: ana@empresa.test\r\nSubject: Hola\r\nDate: Wed, 10 Sep 2026 10:00:00 +0000\r\nMessage-ID: <msg1@x.test>\r\n\r\nPrimer mensaje\r\n",
		strings.ReplaceAll(richMessage, "\n", "\r\n"),
		"From: Proveedor <cobros@proveedor.test>\r\nTo: ana@empresa.test\r\nSubject: Factura mensual\r\nDate: Thu, 11 Sep 2026 10:00:00 +0000\r\n\r\nAdjuntamos la factura mensual.\r\n",
	}
	for _, raw := range msgs {
		if _, err := mb.Append(context.Background(), "INBOX", []byte(raw), nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

const richMessage = `From: "Boletin" <news@tercero.test>
To: ana@empresa.test
Subject: Novedades
Date: Fri, 12 Sep 2026 10:00:00 +0000
Message-ID: <msg2@tercero.test>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="mixed"

--mixed
Content-Type: multipart/related; boundary="rel"

--rel
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain; charset=utf-8

Hola Ana
--alt
Content-Type: text/html; charset=utf-8

<p onclick="robar()">Hola <b>Ana</b></p><script>alert(1)</script><img src="https://tracker.test/p.gif"><img src="cid:logo@tercero.test"><a href="javascript:alert(2)">x</a>
--alt--
--rel
Content-Type: image/png
Content-ID: <logo@tercero.test>
Content-Disposition: inline; filename="logo.png"
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--rel--
--mixed
Content-Type: application/pdf; name="informe.pdf"
Content-Disposition: attachment; filename="informe.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--mixed--
`

// staticAuth hace de mail-auth para el unico buzon de la prueba.
type staticAuth struct{}

func (staticAuth) Verify(_ context.Context, username, pass, _ string) (domain.Identity, error) {
	if username == mailbox && pass == password {
		return domain.Identity{Username: username, DisplayName: "Ana Perez"}, nil
	}
	return domain.Identity{}, domain.ErrInvalidCredentials
}

type memSessions struct {
	mu      sync.Mutex
	m       map[string]domain.Session
	revoked map[string]time.Time
}

func newMemSessions() *memSessions {
	return &memSessions{m: map[string]domain.Session{}, revoked: map[string]time.Time{}}
}

func (s *memSessions) Create(_ context.Context, key string, sess domain.Session, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = sess
	return nil
}
func (s *memSessions) Get(_ context.Context, key string) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.m[key]; ok {
		return sess, nil
	}
	return domain.Session{}, domain.ErrSessionInvalid
}
func (s *memSessions) Touch(context.Context, string, time.Duration) error { return nil }
func (s *memSessions) Delete(_ context.Context, key, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
func (s *memSessions) Revoke(_ context.Context, username string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[username] = at
	return nil
}
func (s *memSessions) RevokedAt(_ context.Context, username string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[username], nil
}

// staticDirectory hace de mail-directory: los remitentes que la regla de Postfix le da al buzon.
type staticDirectory struct{ ids []string }

func (d staticDirectory) SenderIdentities(context.Context, string) ([]string, error) {
	return d.ids, nil
}

func (d staticDirectory) Vacation(context.Context, string) (domain.Vacation, error) {
	return domain.Vacation{}, nil
}

func (d staticDirectory) SetVacation(context.Context, string, domain.VacationInput) (domain.Vacation, error) {
	return domain.Vacation{}, nil
}

func (d staticDirectory) Search(context.Context, string, string, int) ([]domain.AddressBookEntry, error) {
	return nil, nil
}

// memLedger es el registro de envios en memoria (el de Redis tiene su propia prueba).
type memLedger struct {
	mu  sync.Mutex
	m   map[string]domain.SendRecord
	seq int
}

func newMemLedger() *memLedger { return &memLedger{m: map[string]domain.SendRecord{}} }

func (l *memLedger) Reserve(_ context.Context, key string, rec domain.SendRecord, _ time.Duration) (domain.SendRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.m[key]; ok {
		return current, false, nil
	}
	l.seq++
	rec.Token = fmt.Sprintf("marca-%d", l.seq)
	l.m[key] = rec
	return rec, true, nil
}

func (l *memLedger) Update(_ context.Context, key string, rec domain.SendRecord, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.m[key]; !ok || current.Token != rec.Token {
		return false, nil
	}
	l.m[key] = rec
	return true, nil
}

func (l *memLedger) Release(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.m[key]; ok && current.Token == token {
		delete(l.m, key)
	}
	return nil
}

// testTLS genera un certificado autofirmado para localhost y 127.0.0.1.
func testTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	server := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	client := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	return server, client
}

// startIMAP levanta imapmemserver con TLS implicito. El usuario se llama como el inicio
// maestro de Dovecot (buzon*maestro) con la contrasena maestra: es lo que el adaptador
// envia y lo que Dovecot resuelve al buzon real.
func startIMAP(t *testing.T, tlsCfg *tls.Config) string {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(mailbox+"*"+masterUser, masterPass)
	for _, name := range []string{"INBOX", "Sent", "Drafts", "Trash", "Archive"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imaplib.CapSet{
			imaplib.CapIMAP4rev1: {}, imaplib.CapUIDPlus: {}, imaplib.CapMove: {}, imaplib.CapNamespace: {},
			imaplib.CapListExtended: {}, imaplib.CapListStatus: {}, imaplib.CapUnselect: {}, imaplib.CapChildren: {},
		},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, tlsCfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

type delivery struct {
	authUser string
	from     string
	rcpts    []string
	data     []byte
}

// smtpServer es un submission minimo: STARTTLS obligatorio antes de AUTH PLAIN, nombre
// SASL del buzon (lo que Dovecot devuelve a Postfix para usuario*maestro) y rechazo en
// RCPT de un remitente que el buzon no posee, como reject_authenticated_sender_login_mismatch
// con smtpd_delay_reject.
type smtpServer struct {
	addr   string
	tlsCfg *tls.Config
	mu     sync.Mutex
	msgs   []delivery
	// drop cierra la conexion tras recibir DATA sin responder, como un corte de red
	// despues del punto final.
	drop bool
}

func (s *smtpServer) setDropAfterData(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop = v
}

func startSMTP(t *testing.T, tlsCfg *tls.Config) *smtpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &smtpServer{addr: ln.Addr().String(), tlsCfg: tlsCfg}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return s
}

func (s *smtpServer) received() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]delivery(nil), s.msgs...)
}

func (s *smtpServer) handle(conn net.Conn) {
	defer conn.Close()
	tp := textproto.NewConn(conn)
	_ = tp.PrintfLine("220 localhost ESMTP prueba")
	secure := false
	var authUser, from string
	var rcpts []string
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			ext := "250-STARTTLS"
			if secure {
				ext = "250-AUTH PLAIN"
			}
			_ = tp.PrintfLine("250-localhost")
			_ = tp.PrintfLine("%s", ext)
			_ = tp.PrintfLine("250 8BITMIME")
		case "STARTTLS":
			_ = tp.PrintfLine("220 2.0.0 Ready to start TLS")
			tconn := tls.Server(conn, s.tlsCfg)
			if err := tconn.Handshake(); err != nil {
				return
			}
			conn, tp, secure = tconn, textproto.NewConn(tconn), true
		case "AUTH":
			fields := strings.Fields(line)
			if !secure || len(fields) != 3 || !strings.EqualFold(fields[1], "PLAIN") {
				_ = tp.PrintfLine("530 5.7.0 Must issue a STARTTLS command first")
				continue
			}
			raw, _ := base64.StdEncoding.DecodeString(fields[2])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) != 3 || parts[1] != mailbox+"*"+masterUser || parts[2] != masterPass {
				_ = tp.PrintfLine("535 5.7.8 Error: authentication failed")
				continue
			}
			authUser = strings.SplitN(parts[1], "*", 2)[0]
			_ = tp.PrintfLine("235 2.7.0 Authentication successful")
		case "MAIL":
			if authUser == "" {
				_ = tp.PrintfLine("530 5.7.0 Authentication required")
				continue
			}
			from, rcpts = address(line), nil
			_ = tp.PrintfLine("250 2.1.0 Ok")
		case "RCPT":
			if from != authUser {
				_ = tp.PrintfLine("553 5.7.1 <%s>: Sender address rejected: not owned by user %s", from, authUser)
				continue
			}
			rcpts = append(rcpts, address(line))
			_ = tp.PrintfLine("250 2.1.5 Ok")
		case "DATA":
			_ = tp.PrintfLine("354 End data with <CR><LF>.<CR><LF>")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, delivery{authUser: authUser, from: from, rcpts: rcpts, data: data})
			drop := s.drop
			s.mu.Unlock()
			if drop {
				return
			}
			_ = tp.PrintfLine("250 2.0.0 Ok: queued")
		case "RSET":
			from, rcpts = "", nil
			_ = tp.PrintfLine("250 2.0.0 Ok")
		case "QUIT":
			_ = tp.PrintfLine("221 2.0.0 Bye")
			return
		default:
			_ = tp.PrintfLine("502 5.5.2 Error: command not recognized")
		}
	}
}

func address(line string) string {
	start, end := strings.IndexByte(line, '<'), strings.IndexByte(line, '>')
	if start < 0 || end < start {
		return ""
	}
	return strings.ToLower(line[start+1 : end])
}
