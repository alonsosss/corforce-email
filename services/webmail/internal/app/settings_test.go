package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestLaFirmaSeSaneaYLlevaSuTexto(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	sig, err := h.svc.SetSignature(context.Background(), sess, true, "<b>Ana</b><script>x</script>", true)
	if err != nil {
		t.Fatal(err)
	}
	in := h.directory.signatureIn
	if h.directory.settingsFor != testUser || in.HTML != "limpio:<b>Ana</b><script>x</script>" || in.Text != "texto plano" || !in.OnReplies {
		t.Fatalf("lo que llega al directorio: %+v", in)
	}
	if !sig.Enabled || sig.Text != "texto plano" {
		t.Fatalf("%+v", sig)
	}
	if _, err := h.svc.SetSignature(context.Background(), sess, false, "   ", false); err != nil || h.directory.signatureIn.HTML != "" || h.directory.signatureIn.Text != "" {
		t.Fatalf("firma vacia: %+v %v", h.directory.signatureIn, err)
	}
}

func TestLosRechazosDelDirectorioConservanElCampo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.settingsErr = domain.NewValidationError("rules[3].conditions[0].value", "demasiado largo")
	_, err := h.svc.SetFilters(context.Background(), sess, domain.MailFiltersInput{}, domain.Reauthentication{}, testIP)
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || verr.Field != "rules[3].conditions[0].value" {
		t.Fatalf("%v", err)
	}
	h.directory.settingsErr = errors.New("conexion rehusada")
	if _, err := h.svc.Filters(context.Background(), sess); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("fallo del directorio: %v", err)
	}
	if _, err := h.svc.SetSignature(context.Background(), sess, true, "x", false); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("fallo del directorio en la firma: %v", err)
	}
}

func TestLasReglasViajanConElBuzonDeLaSesion(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	keep := true
	in := domain.MailFiltersInput{
		Rules: []domain.FilterRule{{Name: "Facturas", Enabled: true, Match: "any",
			Conditions: []domain.FilterCondition{{Field: "from", Op: "contains", Value: "facturas"}},
			Actions:    []domain.FilterAction{{Type: "move", Folder: "Facturas"}, {Type: "forward", Address: "c@x.pe", KeepCopy: &keep}}}},
		Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"yo@x.pe"}, KeepCopy: true},
	}
	out, err := h.svc.SetFilters(context.Background(), sess, in, domain.Reauthentication{}, testIP)
	if err != nil || h.directory.settingsFor != testUser || len(out.Rules) != 1 || out.Rules[0].Actions[1].Address != "c@x.pe" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestCambiarContrasenaCompruebaLaActual(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	calls := h.auth.calls

	if err := h.svc.ChangePassword(ctx, sess, "mala", "nueva-larga-segura", "", testIP); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("actual incorrecta: %v", err)
	}
	if h.auth.calls != calls+1 || h.auth.lastIP != testIP || h.directory.passwordSet != "" {
		t.Fatalf("se verifica con mail-auth y con la IP real sin cambiar nada: %d %q %q", h.auth.calls, h.auth.lastIP, h.directory.passwordSet)
	}
	if revoked := h.store.revoked[testUser]; !revoked.IsZero() {
		t.Fatal("una contrasena actual mala no revoca nada")
	}

	var verr *domain.ValidationError
	for _, next := range []string{"", testPass} {
		if err := h.svc.ChangePassword(ctx, sess, testPass, next, "", testIP); !errors.As(err, &verr) || verr.Field != "new_password" {
			t.Errorf("nueva %q: %v", next, err)
		}
	}

	h.directory.passwordErr = domain.NewValidationError("password", "demasiado corta")
	if err := h.svc.ChangePassword(ctx, sess, testPass, "corta-pero-no-tanto", "", testIP); !errors.As(err, &verr) || verr.Field != "new_password" || verr.Reason != "demasiado corta" {
		t.Fatalf("politica del directorio: %v", err)
	}
	h.directory.passwordErr = nil

	h.clock.Advance(time.Second)
	if err := h.svc.ChangePassword(ctx, sess, testPass, "nueva-larga-segura", "", testIP); err != nil {
		t.Fatal(err)
	}
	if h.directory.passwordFor != testUser || h.directory.passwordSet != "nueva-larga-segura" {
		t.Fatalf("cambio: %q %q", h.directory.passwordFor, h.directory.passwordSet)
	}
	if !sess.RevokedBy(h.store.revoked[testUser]) {
		t.Fatal("la sesion que cambio la contrasena queda revocada")
	}
}

func TestCambiarContrasenaConMailAuthCaido(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.auth.err = fmt.Errorf("%w: mail-auth caido", domain.ErrUnavailable)
	if err := h.svc.ChangePassword(context.Background(), sess, testPass, "nueva-larga-segura", "", testIP); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if h.directory.passwordSet != "" {
		t.Fatal("sin verificar la actual no se cambia")
	}
}

const (
	testTenant  = "11111111-1111-4111-8111-111111111111"
	testMailbox = "22222222-2222-4222-8222-222222222222"
)

func davSession() domain.Session {
	return domain.Session{Username: testUser, TenantID: testTenant, MailboxID: testMailbox}
}

func TestLibretaYCalendarioUsanLaEmpresaYElBuzonDeLaSesion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sess := davSession()
	if _, err := h.svc.ListContacts(ctx, sess, domain.ContactQuery{Search: "ana", Page: 2}); err != nil {
		t.Fatal(err)
	}
	if h.dav.mb.TenantID != testTenant || h.dav.mb.MailboxID != testMailbox || h.dav.query.Search != "ana" || h.dav.query.Page != 2 {
		t.Fatalf("%+v %+v", h.dav.mb, h.dav.query)
	}
	if _, err := h.svc.UpdateContact(ctx, sess, "c1", domain.ContactInput{Name: "Ana"}, `"v2"`); err != nil || h.dav.id != "c1" || h.dav.ifMatch != `"v2"` {
		t.Fatalf("%v %q %q", err, h.dav.id, h.dav.ifMatch)
	}
	w, _ := domain.NewEventWindow("2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z")
	if occ, err := h.svc.Occurrences(ctx, sess, w); err != nil || len(occ) != 1 || !h.dav.window.Start.Equal(w.Start) {
		t.Fatalf("%+v %v", occ, err)
	}
	if _, err := h.svc.DeleteEvent(ctx, sess, "e1", true); err != nil || h.dav.id != "e1" {
		t.Fatalf("%v %q", err, h.dav.id)
	}
}

func TestUnaSesionSinEmpresaVuelveAlInicioDeSesion(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	if _, err := h.svc.ListContacts(ctx, sess, domain.ContactQuery{}); !errors.Is(err, domain.ErrSessionInvalid) {
		t.Fatalf("contactos: %v", err)
	}
	if _, err := h.svc.CreateEvent(ctx, sess, domain.EventInput{}, true); !errors.Is(err, domain.ErrSessionInvalid) {
		t.Fatalf("eventos: %v", err)
	}
	if h.dav.calls != 0 {
		t.Fatal("sin empresa ni buzon no se llama a mail-dav")
	}
}

func TestLibretaValidaIdsYTopes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sess := davSession()
	var verr *domain.ValidationError
	if _, err := h.svc.Contact(ctx, sess, "../../internal"); !errors.As(err, &verr) {
		t.Fatalf("id: %v", err)
	}
	if _, err := h.svc.UpdateEvent(ctx, sess, "e1", domain.EventInput{}, "\"x\"\r\nX-Mailbox-ID: otro", true); !errors.As(err, &verr) {
		t.Fatalf("If-Match con salto de linea: %v", err)
	}
	if _, err := h.svc.ImportContacts(ctx, sess, "a.vcf", make([]byte, 2<<10)); !errors.Is(err, domain.ErrImportTooLarge) {
		t.Fatalf("tope de importacion: %v", err)
	}
	if _, err := h.svc.ImportContacts(ctx, sess, "a.vcf", nil); !errors.As(err, &verr) {
		t.Fatalf("fichero vacio: %v", err)
	}
	if h.dav.calls != 0 {
		t.Fatal("nada invalido llega a mail-dav")
	}
	if res, err := h.svc.ImportContacts(ctx, sess, "a.vcf", []byte("BEGIN:VCARD")); err != nil || res.Imported != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestLosRechazosDeMailDavPasanTalCual(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sess := davSession()
	for _, kind := range []domain.RejectionKind{domain.RejectNotFound, domain.RejectPrecondition, domain.RejectQuota, domain.RejectValidation, domain.RejectRateLimited, domain.RejectUnavailable} {
		rejection := &domain.ServiceRejection{Kind: kind, Code: "CODIGO", Message: "motivo", Details: map[string]string{"limit": "10"}}
		h.dav.err = rejection
		_, err := h.svc.Contact(ctx, sess, "c1")
		var got *domain.ServiceRejection
		if !errors.As(err, &got) || got != rejection {
			t.Errorf("%d: %v", kind, err)
		}
	}
	h.dav.err = &domain.ServiceRejection{Kind: domain.RejectPrecondition, Code: "PRECONDITION_FAILED"}
	if _, err := h.svc.UpdateEvent(ctx, sess, "e1", domain.EventInput{}, `"v1"`, true); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("una precondicion se reconoce como tal: %v", err)
	}
	h.dav.err = errors.New("conexion rehusada")
	if _, err := h.svc.Event(ctx, sess, "e1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	h.dav.err = nil
	if limits, err := h.svc.DAVLimits(ctx, sess); err != nil || limits["max_import_cards"] != 1000 {
		t.Fatalf("topes: %v %v", limits, err)
	}
	if _, err := h.svc.DAVLimits(ctx, domain.Session{Username: testUser}); !errors.Is(err, domain.ErrSessionInvalid) {
		t.Fatalf("topes sin empresa: %v", err)
	}
}
