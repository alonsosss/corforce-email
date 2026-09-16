package nats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"go.uber.org/zap"
)

type revocation struct {
	username string
	at       time.Time
}

type fakeRevoker struct {
	calls []revocation
	err   error
}

func (f *fakeRevoker) RevokeMailbox(_ context.Context, username string, at time.Time) error {
	f.calls = append(f.calls, revocation{username, at})
	return f.err
}

func handle(c *Consumer, evt events.Event) bool {
	acked := false
	c.Handle(evt, func() { acked = true })
	return acked
}

func TestRevocaConCambiosDelBuzon(t *testing.T) {
	at := time.Date(2026, 9, 13, 9, 30, 0, 0, time.UTC)
	for _, subject := range []string{SubjectMailboxUpdated, SubjectMailboxDeleted, SubjectMailboxCredentialsChanged} {
		rev := &fakeRevoker{}
		c := NewConsumer(nil, rev, zap.NewNop())
		ok := handle(c, events.Event{ID: "e1", Type: subject, Timestamp: at, Data: map[string]any{"username": "Ana@Empresa.PE", "tenant_id": "t"}})
		if !ok || len(rev.calls) != 1 || rev.calls[0].username != "ana@empresa.pe" || !rev.calls[0].at.Equal(at.Add(revocationMargin)) {
			t.Fatalf("%s: ack=%v llamadas=%+v", subject, ok, rev.calls)
		}
	}
}

func TestCredencialesCambiadasUsaChangedAt(t *testing.T) {
	published := time.Date(2026, 9, 13, 9, 30, 0, 0, time.UTC)
	changed := published.Add(3 * time.Second)
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	evt := events.Event{ID: "e2", Type: SubjectMailboxCredentialsChanged, Timestamp: published, Data: map[string]any{
		"tenant_id": "7c1f0e9a-0000-4000-8000-000000000001", "id": "m1", "username": "ana@empresa.pe",
		"changed_at": changed.Format(time.RFC3339),
	}}
	// La misma entrega dos veces revoca exactamente en el mismo instante: idempotente.
	handle(c, evt)
	handle(c, evt)
	if len(rev.calls) != 2 || !rev.calls[0].at.Equal(changed.Add(revocationMargin)) || !rev.calls[1].at.Equal(rev.calls[0].at) {
		t.Fatalf("got %+v", rev.calls)
	}
	// Un changed_at ilegible no bloquea la revocacion: se usa la hora del evento.
	rev.calls = nil
	evt.Data = map[string]any{"username": "ana@empresa.pe", "changed_at": "ayer"}
	handle(c, evt)
	if len(rev.calls) != 1 || !rev.calls[0].at.Equal(published.Add(revocationMargin)) {
		t.Fatalf("changed_at ilegible: %+v", rev.calls)
	}
}

// Una contrasena de aplicacion no abre el webmail: su cambio no cierra sus sesiones. Si las cierran
// la principal, cualquier otro valor y los eventos sin credential (anteriores al campo).
func TestContrasenaDeAplicacionNoRevocaElWebmail(t *testing.T) {
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	evt := events.Event{ID: "e3", Type: SubjectMailboxCredentialsChanged, Data: map[string]any{
		"username": "ana@empresa.pe", "credential": "app_password",
	}}
	if !handle(c, evt) || !handle(c, evt) || len(rev.calls) != 0 {
		t.Fatalf("app_password se confirma sin revocar, tambien repetido: %+v", rev.calls)
	}
	for _, credential := range []any{"password", "otra", 1, nil} {
		rev.calls = nil
		data := map[string]any{"username": "ana@empresa.pe"}
		if credential != nil {
			data["credential"] = credential
		}
		if !handle(c, events.Event{Type: SubjectMailboxCredentialsChanged, Data: data}) || len(rev.calls) != 1 {
			t.Fatalf("credential %v: revocaciones %+v", credential, rev.calls)
		}
	}
	rev.calls = nil
	if !handle(c, events.Event{Type: SubjectMailboxUpdated, Data: map[string]any{"username": "ana@empresa.pe", "credential": "app_password"}}) || len(rev.calls) != 1 {
		t.Fatalf("un cambio del buzon revoca aunque traiga credential: %+v", rev.calls)
	}
}

func TestIgnoraOtrosEventos(t *testing.T) {
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	for _, subject := range []string{"mail.mailbox.created", "mail.domain.updated", "mail.alias.deleted"} {
		if !handle(c, events.Event{Type: subject, Data: map[string]any{"username": "ana@empresa.pe"}}) {
			t.Fatalf("%s se confirma sin hacer nada", subject)
		}
	}
	if len(rev.calls) != 0 {
		t.Fatalf("revocaciones inesperadas: %+v", rev.calls)
	}
}

func TestDescartaUsernameInvalido(t *testing.T) {
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	for _, data := range []any{map[string]any{"username": "ana*webmail@platform.local"}, map[string]any{}, "no es un mapa"} {
		if !handle(c, events.Event{Type: SubjectMailboxUpdated, Data: data}) {
			t.Fatalf("%v: un evento ilegible se confirma para no reentregarse sin fin", data)
		}
	}
	if len(rev.calls) != 0 {
		t.Fatal("un username invalido no revoca nada")
	}
}

func TestFalloDelAlmacenSeReentrega(t *testing.T) {
	rev := &fakeRevoker{err: errors.New("redis caido")}
	c := NewConsumer(nil, rev, zap.NewNop())
	if handle(c, events.Event{Type: SubjectMailboxDeleted, Data: map[string]any{"username": "ana@empresa.pe"}}) {
		t.Fatal("una revocacion no aplicada no se confirma")
	}
}

// Un cambio que el directorio declara inofensivo no cierra ninguna sesion: antes el webmail revocaba
// con cualquier mail.mailbox.updated, que no decia que cambio, y una cuota o un nombre visible
// echaban al usuario de su sesion sin motivo.
func TestUnCambioInofensivoNoRevoca(t *testing.T) {
	for _, changed := range [][]any{
		{"display_name"},
		{"quota_bytes"},
		{"display_name", "quota_bytes"},
		{"kind", "tls_enforce_in", "tls_enforce_out", "relayhost_id", "force_pw_update"},
		// El webmail no usa pop3 ni sieve: perderlos no invalida su sesion aunque si cierre la
		// sesion IMAP o POP3 que los usara (eso lo hace mail-security en Dovecot).
		{"pop3_access", "sieve_access"},
		// Lista vacia: el cambio no toco ningun atributo.
		{},
	} {
		for _, subject := range []string{SubjectMailboxUpdated, SubjectMailboxCredentialsChanged} {
			rev := &fakeRevoker{}
			c := NewConsumer(nil, rev, zap.NewNop())
			evt := events.Event{ID: "e", Type: subject, Data: map[string]any{
				"username": "ana@empresa.pe", "changed": changed,
			}}
			if !handle(c, evt) || len(rev.calls) != 0 {
				t.Fatalf("%s con changed %v: revocaciones %+v", subject, changed, rev.calls)
			}
		}
	}
}

// Lo que invalida de verdad la sesion la cierra al momento, exactamente como antes.
func TestLoQueInvalidaLaSesionSigueRevocando(t *testing.T) {
	for _, changed := range [][]any{
		{"active"},
		{"imap_access"},
		{"smtp_access"},
		{"password"},
		{"app_password"},
		// Basta uno: el resto de la lista es inofensivo.
		{"display_name", "imap_access"},
		{"quota_bytes", "active"},
	} {
		for _, subject := range []string{SubjectMailboxUpdated, SubjectMailboxCredentialsChanged} {
			rev := &fakeRevoker{}
			c := NewConsumer(nil, rev, zap.NewNop())
			evt := events.Event{ID: "e", Type: subject, Data: map[string]any{
				"username": "ana@empresa.pe", "changed": changed,
			}}
			if !handle(c, evt) || len(rev.calls) != 1 {
				t.Fatalf("%s con changed %v: revocaciones %+v", subject, changed, rev.calls)
			}
		}
	}
}

// La lista es de lo inofensivo, no de lo peligroso: sin changed (un mail-directory anterior al
// campo), con un changed ilegible o con un atributo que este consumidor no conoce se revoca, que es
// lo que hacia antes con cualquier cambio. Un borrado revoca diga lo que diga el payload.
func TestAnteLaDudaRevoca(t *testing.T) {
	cases := map[string]map[string]any{
		"sin changed":          {"username": "ana@empresa.pe"},
		"changed nulo":         {"username": "ana@empresa.pe", "changed": nil},
		"changed no es lista":  {"username": "ana@empresa.pe", "changed": "display_name"},
		"changed es un mapa":   {"username": "ana@empresa.pe", "changed": map[string]any{"display_name": true}},
		"elemento no es texto": {"username": "ana@empresa.pe", "changed": []any{"display_name", 1}},
		"atributo desconocido": {"username": "ana@empresa.pe", "changed": []any{"inventado_en_el_futuro"}},
		"atributo vacio":       {"username": "ana@empresa.pe", "changed": []any{""}},
	}
	for name, data := range cases {
		rev := &fakeRevoker{}
		c := NewConsumer(nil, rev, zap.NewNop())
		if !handle(c, events.Event{ID: "e", Type: SubjectMailboxUpdated, Data: data}) || len(rev.calls) != 1 {
			t.Fatalf("%s: revocaciones %+v", name, rev.calls)
		}
	}
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	borrado := events.Event{ID: "e", Type: SubjectMailboxDeleted, Data: map[string]any{
		"username": "ana@empresa.pe", "changed": []any{"display_name"},
	}}
	if !handle(c, borrado) || len(rev.calls) != 1 {
		t.Fatalf("un borrado revoca siempre: %+v", rev.calls)
	}
}

// Una contrasena de aplicacion no abre el webmail ni con un changed que parezca peligroso.
func TestContrasenaDeAplicacionManda(t *testing.T) {
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	evt := events.Event{ID: "e", Type: SubjectMailboxCredentialsChanged, Data: map[string]any{
		"username": "ana@empresa.pe", "credential": "app_password", "changed": []any{"imap_access"},
	}}
	if !handle(c, evt) || len(rev.calls) != 0 {
		t.Fatalf("revocaciones %+v", rev.calls)
	}
}

func TestSinHoraUsaLaActual(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	rev := &fakeRevoker{}
	c := NewConsumer(nil, rev, zap.NewNop())
	c.now = func() time.Time { return now }
	handle(c, events.Event{Type: SubjectMailboxUpdated, Data: map[string]any{"username": "ana@empresa.pe"}})
	if len(rev.calls) != 1 || !rev.calls[0].at.Equal(now.Add(revocationMargin)) {
		t.Fatalf("got %+v", rev.calls)
	}
}
