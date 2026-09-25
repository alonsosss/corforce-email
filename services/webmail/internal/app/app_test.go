package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var ctx = context.Background()

func TestLoginIdenticoParaInexistenteYContrasenaMala(t *testing.T) {
	h := newHarness(t)

	_, _, errUnknown := h.svc.Login(ctx, "nadie@empresa.pe", "loquesea-larga", testIP, "")
	_, _, errWrong := h.svc.Login(ctx, testUser, "mala-contrasena", testIP, "")

	// El mismo error, no uno equivalente: el adaptador HTTP responde con el mismo cuerpo.
	if errUnknown != domain.ErrInvalidCredentials || errWrong != domain.ErrInvalidCredentials {
		t.Fatalf("errores distintos: %v / %v", errUnknown, errWrong)
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("un inicio rechazado no crea sesion")
	}
	// Los dos llegan a mail-auth, que es quien aplica el freno por (buzon, IP).
	if h.auth.calls != 2 || h.auth.lastIP != testIP {
		t.Fatalf("mail-auth: calls=%d ip=%q", h.auth.calls, h.auth.lastIP)
	}
}

func TestLoginConNombreImposibleNoConsultaMailAuth(t *testing.T) {
	h := newHarness(t)
	for _, u := range []string{"ana@empresa.pe*webmail@platform.local", "sin-arroba", ""} {
		if _, _, err := h.svc.Login(ctx, u, testPass, testIP, ""); err != domain.ErrInvalidCredentials {
			t.Fatalf("%q: %v", u, err)
		}
	}
	if h.auth.calls != 0 {
		t.Fatal("un nombre que no puede ser buzon no llega a mail-auth")
	}
}

func TestLoginMailAuthNoDisponible(t *testing.T) {
	h := newHarness(t)
	h.auth.err = fmt.Errorf("%w: conexion rechazada", domain.ErrUnavailable)
	if _, _, err := h.svc.Login(ctx, testUser, testPass, testIP, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("sin verificacion no hay sesion")
	}
}

func TestLoginRotaElTokenYGuardaSoloSuHash(t *testing.T) {
	h := newHarness(t)
	first, _ := h.login(t)
	second, sess, err := h.svc.Login(ctx, testUser, testPass, testIP, first)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("el inicio de sesion debe emitir un token nuevo")
	}
	if _, err := h.svc.Authenticate(ctx, first); err != domain.ErrSessionInvalid {
		t.Fatalf("la sesion previa debe destruirse: %v", err)
	}
	if got, err := h.svc.Authenticate(ctx, second); err != nil || got.Username != testUser || got.DisplayName != "Ana Perez" {
		t.Fatalf("sesion nueva: %+v %v", got, err)
	}
	sum := sha256.Sum256([]byte(second))
	for key := range h.store.sessions {
		if strings.Contains(key, second) || key != hex.EncodeToString(sum[:]) {
			t.Fatalf("la clave del almacen debe ser el hash del token, es %q", key)
		}
	}
	if !sess.CreatedAt.Equal(h.clock.Now()) {
		t.Fatalf("la sesion nace al empezar la verificacion: %v", sess.CreatedAt)
	}
}

func TestAuthenticateCaducaPorInactividad(t *testing.T) {
	h := newHarness(t)
	token, _ := h.login(t)
	for i := 0; i < 3; i++ {
		h.clock.Advance(29 * time.Minute)
		if _, err := h.svc.Authenticate(ctx, token); err != nil {
			t.Fatalf("la actividad renueva la inactividad (%d): %v", i, err)
		}
	}
	h.clock.Advance(31 * time.Minute)
	if _, err := h.svc.Authenticate(ctx, token); err != domain.ErrSessionInvalid {
		t.Fatalf("sin actividad durante la inactividad la sesion caduca: %v", err)
	}
}

func TestAuthenticateCaducaPorVidaMaxima(t *testing.T) {
	h := newHarness(t)
	h.store.ignoreTTL = true // la vida maxima no puede depender del TTL del almacen
	token, _ := h.login(t)
	elapsed := time.Duration(0)
	for elapsed+15*time.Minute < 12*time.Hour {
		h.clock.Advance(15 * time.Minute)
		elapsed += 15 * time.Minute
		if _, err := h.svc.Authenticate(ctx, token); err != nil {
			t.Fatalf("a las %v: %v", elapsed, err)
		}
	}
	if last := h.store.touches[len(h.store.touches)-1]; last != 15*time.Minute {
		t.Fatalf("cerca del maximo el TTL se recorta a lo que queda: %v", last)
	}
	h.clock.Advance(15 * time.Minute)
	if _, err := h.svc.Authenticate(ctx, token); err != domain.ErrSessionInvalid {
		t.Fatalf("con actividad continua la sesion caduca a las 12h: %v", err)
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("la sesion caducada se borra")
	}
}

func TestRevocacionPorEventoDeBuzon(t *testing.T) {
	h := newHarness(t)
	token, sess := h.login(t)
	h.clock.Advance(time.Minute)
	if err := h.svc.RevokeMailbox(ctx, testUser, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(ctx, token); err != domain.ErrSessionInvalid {
		t.Fatalf("la sesion revocada no debe pasar: %v", err)
	}

	h.clock.Advance(time.Minute)
	fresh, _ := h.login(t)
	if _, err := h.svc.Authenticate(ctx, fresh); err != nil {
		t.Fatalf("una sesion abierta despues de la revocacion vale: %v", err)
	}
	// La reentrega tardia del mismo evento no alcanza a la sesion nueva.
	if err := h.svc.RevokeMailbox(ctx, testUser, sess.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(ctx, fresh); err != nil {
		t.Fatalf("una revocacion vieja no invalida sesiones posteriores: %v", err)
	}
}

func TestAuthenticateFallaCerradoSinAlmacen(t *testing.T) {
	h := newHarness(t)
	token, _ := h.login(t)
	h.store.failRevokedAt = errors.New("redis caido")
	if _, err := h.svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("sin poder comprobar la revocacion no se sirve el buzon: %v", err)
	}
	h.store.failRevokedAt = nil
	h.store.failGet = errors.New("redis caido")
	if _, err := h.svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestAuthenticateRechazaTokensMalformados(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{"", "corto", strings.Repeat("a", 44), strings.Repeat("*", 43)} {
		if _, err := h.svc.Authenticate(ctx, tok); err != domain.ErrSessionInvalid {
			t.Fatalf("%q: %v", tok, err)
		}
	}
}

func TestLogoutEsIdempotente(t *testing.T) {
	h := newHarness(t)
	token, _ := h.login(t)
	for i := 0; i < 2; i++ {
		if err := h.svc.Logout(ctx, token); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.Authenticate(ctx, token); err != domain.ErrSessionInvalid {
		t.Fatalf("tras el cierre la sesion no vale: %v", err)
	}
}

func TestSendRemitenteBccYCopiaEnEnviados(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	res, err := h.svc.Send(ctx, sess, domain.Draft{
		To: []domain.Address{{Email: "luis@x.com"}}, Bcc: []domain.Address{{Email: "oculto@x.com"}},
		Subject: "Hola", Text: "cuerpo",
	}, sendOpts(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.sender.calls) != 1 {
		t.Fatalf("envios: %d", len(h.sender.calls))
	}
	call := h.sender.calls[0]
	if call.username != testUser || call.from != testUser || strings.Join(call.rcpts, ",") != "luis@x.com,oculto@x.com" {
		t.Fatalf("sobre inesperado: %+v", call)
	}
	if !bytes.HasPrefix(call.raw, []byte("bcc=false")) {
		t.Fatal("el mensaje entregado no lleva la cabecera Bcc")
	}
	if h.composer.last.From != (domain.Address{Name: "Ana Perez", Email: testUser}) {
		t.Fatalf("remitente por defecto: %+v", h.composer.last.From)
	}
	if len(h.mb.appended) != 1 || h.mb.appended[0].folder != "Sent" || !bytes.HasPrefix(h.mb.appended[0].raw, []byte("bcc=true")) ||
		len(h.mb.appended[0].flags) != 1 || h.mb.appended[0].flags[0] != domain.FlagSeen {
		t.Fatalf("copia en Enviados: %+v", h.mb.appended)
	}
	if !res.SavedToSent || !strings.HasSuffix(res.MessageID, "@empresa.pe") {
		t.Fatalf("resultado: %+v", res)
	}
}

func TestSendAplicaLosLimites(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)

	many := domain.Draft{To: []domain.Address{{Email: "a@x.com"}, {Email: "b@x.com"}, {Email: "c@x.com"}, {Email: "d@x.com"}}}
	if _, err := h.svc.Send(ctx, sess, many, sendOpts(0)); !errors.Is(err, domain.ErrTooManyRecipients) {
		t.Fatalf("destinatarios: %v", err)
	}

	h.composer.size = 2000
	if _, err := h.svc.Send(ctx, sess, domain.Draft{To: []domain.Address{{Email: "a@x.com"}}, Text: "x"}, sendOpts(0)); !errors.Is(err, domain.ErrMessageTooLarge) {
		t.Fatalf("tamano del mensaje compuesto: %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("nada que supere un limite llega a Postfix")
	}
}

func TestSendAnalizaLosAdjuntos(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	draft := domain.Draft{
		To:          []domain.Address{{Email: "a@x.com"}},
		Attachments: []domain.Attachment{{Filename: "../factura.exe", ContentType: "application/x-msdownload; x=\"y\"", Data: []byte("MZ")}},
	}

	h.scanner.err = fmt.Errorf("%w: Eicar-Test-Signature", domain.ErrAttachmentInfected)
	if _, err := h.svc.Send(ctx, sess, draft, sendOpts(0)); err != domain.ErrAttachmentInfected {
		t.Fatalf("adjunto infectado: %v", err)
	}
	h.scanner.err = fmt.Errorf("%w: clamd caido", domain.ErrScanUnavailable)
	if _, err := h.svc.Send(ctx, sess, draft, sendOpts(0)); err != domain.ErrScanUnavailable {
		t.Fatalf("sin analisis no se envia: %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("un adjunto sin analizar o infectado no sale")
	}
	if h.scanner.scanned[0] != "factura.exe" {
		t.Fatalf("el nombre llega saneado al analisis: %q", h.scanner.scanned[0])
	}
}

func TestSendAnalizaLasImagenesDelCuerpo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.sanitizer.inline = []domain.InlineImage{{ContentID: "abc@x.com", ContentType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}}
	draft := domain.Draft{To: []domain.Address{{Email: "a@x.com"}}, HTML: `<img src="cid:abc@x.com">`}

	h.scanner.err = fmt.Errorf("%w: Eicar-Test-Signature", domain.ErrAttachmentInfected)
	if _, err := h.svc.Send(ctx, sess, draft, sendOpts(0)); err != domain.ErrAttachmentInfected {
		t.Fatalf("imagen infectada: %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("una imagen del cuerpo con malware no sale")
	}
	if len(h.scanner.scanned) != 1 || h.scanner.scanned[0] != "imagen-abc.png" {
		t.Fatalf("la imagen pasa por ClamAV: %v", h.scanner.scanned)
	}
}

func TestSendRespuestaEncadenaYMarcaRespondido(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.reply = domain.ReplyReference{MessageID: "orig@x.com", References: []string{"a@x.com", "no valido"}}
	_, err := h.svc.Send(ctx, sess, domain.Draft{
		To: []domain.Address{{Email: "luis@x.com"}}, Text: "ok",
		InReplyTo: &domain.ReplyTarget{Folder: "INBOX", UID: 9},
	}, sendOpts(0))
	if err != nil {
		t.Fatal(err)
	}
	if h.composer.last.InReplyTo != "orig@x.com" || strings.Join(h.composer.last.References, ",") != "a@x.com,orig@x.com" {
		t.Fatalf("cadena: %q %v", h.composer.last.InReplyTo, h.composer.last.References)
	}
	if len(h.mb.flagged) != 1 || h.mb.flagged[0].Add[0] != domain.FlagAnswered {
		t.Fatalf("el original se marca respondido: %+v", h.mb.flagged)
	}
}

// Aunque el directorio de la celda permita el remitente (un cambio que el webmail ve antes
// que Postfix, por ejemplo), Postfix sigue siendo la ultima palabra: su rechazo llega tal
// cual y no se guarda nada.
func TestSendRemitenteQuePostfixRechaza(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.ids = []string{"director@empresa.pe"}
	h.sender.err = fmt.Errorf("%w: 553 Sender address rejected", domain.ErrSenderNotAllowed)
	_, err := h.svc.Send(ctx, sess, domain.Draft{From: domain.Address{Email: "director@empresa.pe"}, To: []domain.Address{{Email: "a@x.com"}}}, sendOpts(0))
	if !errors.Is(err, domain.ErrSenderNotAllowed) {
		t.Fatalf("got %v", err)
	}
	if h.sender.calls[0].from != "director@empresa.pe" {
		t.Fatal("el remitente permitido llega a Postfix, que vuelve a decidir")
	}
	if len(h.mb.appended) != 0 {
		t.Fatal("un envio rechazado no se guarda en Enviados")
	}
}

func TestSaveDraftReemplazaElAnterior(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	uid, err := h.svc.SaveDraft(ctx, sess, domain.Draft{Subject: "Borrador"}, 5)
	if err != nil || uid != 7 {
		t.Fatalf("uid=%d err=%v", uid, err)
	}
	a := h.mb.appended
	if len(a) != 1 || a[0].folder != "Drafts" || len(a[0].flags) != 2 || a[0].flags[0] != domain.FlagDraft {
		t.Fatalf("borrador: %+v", a)
	}
	if len(h.mb.expunged) != 1 || h.mb.expunged[0] != "Drafts:5" {
		t.Fatalf("el borrador anterior se retira: %v", h.mb.expunged)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("guardar un borrador no envia nada")
	}
}

func TestDeleteMueveAPapeleraYBorraDesdePapelera(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	if permanent, err := h.svc.Delete(ctx, sess, "INBOX", 3); err != nil || permanent {
		t.Fatalf("desde INBOX: permanent=%v err=%v", permanent, err)
	}
	if permanent, err := h.svc.Delete(ctx, sess, "Trash", 4); err != nil || !permanent {
		t.Fatalf("desde la papelera: permanent=%v err=%v", permanent, err)
	}
	if strings.Join(h.mb.moved, ",") != "INBOX:3->Trash" || strings.Join(h.mb.expunged, ",") != "Trash:4" {
		t.Fatalf("moved=%v expunged=%v", h.mb.moved, h.mb.expunged)
	}
	h.mb.folders = h.mb.folders[:3]
	if _, err := h.svc.Delete(ctx, sess, "INBOX", 3); err != domain.ErrTrashNotFound {
		t.Fatalf("sin papelera: %v", err)
	}
}

func TestReadMessageResuelveCIDYBloqueaRemotas(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.raw = &domain.RawMessage{HTML: `<img src="cid:img1@x">`, Parts: []domain.Part{{ID: "2", ContentID: "img1@x"}}}
	h.sanitizer.remote = true

	msg, err := h.svc.ReadMessage(ctx, sess, "INBOX", 9, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.RemoteImages.Present || !msg.RemoteImages.Blocked || msg.HTML != `limpio:<img src="cid:img1@x">` {
		t.Fatalf("mensaje: %+v", msg)
	}
	if u, ok := h.sanitizer.opts.ResolveCID("<img1@x>"); !ok || u != "/parts/INBOX/9/2" {
		t.Fatalf("cid: %q %v", u, ok)
	}
	if _, ok := h.sanitizer.opts.ResolveCID("otra@x"); ok {
		t.Fatal("un cid desconocido no se resuelve")
	}
	if h.sanitizer.opts.AllowRemoteImages {
		t.Fatal("las imagenes remotas se bloquean por defecto")
	}

	msg, _ = h.svc.ReadMessage(ctx, sess, "INBOX", 9, true, true)
	if msg.RemoteImages.Blocked || !h.sanitizer.opts.AllowRemoteImages {
		t.Fatal("remote_images=allow levanta el bloqueo solo en esa lectura")
	}
}

func TestMoveValidaDestino(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	var verr *domain.ValidationError
	if err := h.svc.Move(ctx, sess, "INBOX", 3, "INBOX"); !errors.As(err, &verr) {
		t.Fatalf("mismo destino: %v", err)
	}
	if err := h.svc.Move(ctx, sess, "INBOX", 3, "Otra\r\nA1 DELETE INBOX"); !errors.As(err, &verr) || verr.Field != "to" {
		t.Fatalf("destino con CRLF: %v", err)
	}
	if h.mail.opened != 0 {
		t.Fatal("una peticion invalida no abre el buzon")
	}
}
