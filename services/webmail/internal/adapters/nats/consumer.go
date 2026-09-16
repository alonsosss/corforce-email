// Package nats revoca las sesiones del webmail con los eventos del directorio de correo.
package nats

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	// StreamName y StreamSubjects repiten la declaracion de mail-directory, dueno de
	// mail.>: el consumidor asegura el stream antes de suscribirse para no depender de
	// que el productor arranque primero.
	StreamName     = "MAIL_DIRECTORY"
	StreamSubjects = "mail.>"
	// MailboxSubjects filtra el consumidor a los eventos de buzon.
	MailboxSubjects = "mail.mailbox.>"
	ConsumerName    = "webmail-sessions"

	SubjectMailboxUpdated = "mail.mailbox.updated"
	SubjectMailboxDeleted = "mail.mailbox.deleted"
	// SubjectMailboxCredentialsChanged es una credencial del buzon que dejo de valer.
	// Payload: tenant_id, id, username, changed_at (RFC 3339, UTC), credential y changed.
	SubjectMailboxCredentialsChanged = "mail.mailbox.credentials_changed"
	// credentialAppPassword marca el cambio de una contrasena de aplicacion. El webmail solo
	// admite la principal (mail-auth, service webmail), asi que ninguna de sus sesiones entro
	// con ella. Cualquier otro valor, o ninguno (eventos anteriores al campo), es la principal.
	credentialAppPassword = "app_password"
	// changedField es la lista de atributos que el directorio dice haber cambiado.
	changedField = "changed"

	subscribeRetry = 5 * time.Second
	handleTimeout  = 15 * time.Second
	// revocationMargin amplia la marca de revocacion mas alla del instante del cambio:
	// cubre la duracion de la transaccion que lo confirma y el desfase de reloj entre
	// mail-directory y el webmail. El coste es que quien entre con la contrasena nueva en
	// esos segundos tiene que volver a entrar.
	revocationMargin = 2 * time.Second
)

// harmlessChanges son los atributos del buzon cuyo cambio NO invalida una sesion del webmail: no
// tocan su contrasena principal ni lo que necesita para servir el buzon (active = 1 con
// imap_access y smtp_access, que mail-auth exige al abrir la sesion). pop3_access y sieve_access
// estan porque el webmail no usa ninguno de los dos, y kind, relayhost_id y tls_enforce_* porque
// gobiernan entrega y enrutado, no la autenticacion.
//
// Es una lista de lo inofensivo, no de lo peligroso: un atributo nuevo, o cualquier nombre que
// este consumidor no reconozca, cae del lado que revoca. Ampliarla es una decision consciente.
var harmlessChanges = map[string]bool{
	"display_name": true, "quota_bytes": true, "kind": true,
	"tls_enforce_in": true, "tls_enforce_out": true, "relayhost_id": true,
	"force_pw_update": true, "pop3_access": true, "sieve_access": true,
}

// Revoker es lo que el consumidor necesita del caso de uso.
type Revoker interface {
	RevokeMailbox(ctx context.Context, username string, at time.Time) error
}

// Consumer revoca las sesiones de un buzon cuando cambia su contrasena principal, deja de poder
// entrar, se borra o pierde lo que el webmail necesita. Un cambio que el directorio declara
// inofensivo (cuota, nombre visible) no cierra ninguna sesion; el siguiente inicio de sesion lo
// vuelve a comprobar todo en mail-auth.
type Consumer struct {
	bus     *events.Bus
	revoker Revoker
	logger  *zap.Logger
	now     func() time.Time
}

func NewConsumer(bus *events.Bus, revoker Revoker, logger *zap.Logger) *Consumer {
	return &Consumer{bus: bus, revoker: revoker, logger: logger, now: time.Now}
}

// Run bloquea hasta suscribirse o hasta que el contexto se cancele; si NATS no responde
// reintenta. Mientras no haya suscripcion las sesiones siguen acotadas por su vida maxima.
func (c *Consumer) Run(ctx context.Context) {
	if c.bus == nil {
		return
	}
	for {
		err := c.bus.EnsureStream(StreamName, []string{StreamSubjects})
		if err == nil {
			_, err = c.bus.DurableQueueSubscribe(MailboxSubjects, ConsumerName, c.Handle)
		}
		if err == nil {
			c.logger.Info("webmail: suscrito a los eventos de buzon", zap.String("consumer", ConsumerName))
			return
		}
		c.logger.Warn("webmail: no se pudo suscribir a los eventos de buzon; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(subscribeRetry):
		}
	}
}

// Handle es idempotente por evento: la marca de revocacion sale solo del propio evento
// (su changed_at o su hora) y nunca retrocede, y solo se borran las sesiones abiertas
// hasta ella; procesar dos veces el mismo evento deja exactamente lo mismo y no cierra
// sesiones abiertas despues del cambio.
func (c *Consumer) Handle(evt events.Event, ack func()) {
	switch evt.Type {
	case SubjectMailboxUpdated, SubjectMailboxDeleted, SubjectMailboxCredentialsChanged:
	default:
		ack()
		return
	}
	data, _ := evt.Data.(map[string]any)
	raw, _ := data["username"].(string)
	username, ok := domain.NormalizeUsername(raw)
	if !ok {
		c.logger.Warn("webmail: evento de buzon sin username valido; se descarta", zap.String("subject", evt.Type), zap.String("event_id", evt.ID))
		ack()
		return
	}
	if evt.Type == SubjectMailboxCredentialsChanged && data["credential"] == credentialAppPassword {
		c.logger.Info("webmail: cambio de una contrasena de aplicacion; las sesiones del webmail siguen",
			zap.String("username", username), zap.String("event_id", evt.ID))
		ack()
		return
	}
	// Un borrado nunca se salta la revocacion, diga lo que diga el resto del payload.
	if evt.Type != SubjectMailboxDeleted && sessionSurvives(data) {
		c.logger.Info("webmail: cambio del buzon que no invalida la sesion; las sesiones del webmail siguen",
			zap.String("subject", evt.Type), zap.String("username", username),
			zap.Any("changed", data[changedField]), zap.String("event_id", evt.ID))
		ack()
		return
	}
	at := c.revokedAt(evt, data)
	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	if err := c.revoker.RevokeMailbox(ctx, username, at); err != nil {
		c.logger.Error("webmail: revocacion no aplicada; se reentregara", zap.String("subject", evt.Type),
			zap.String("username", username), zap.Error(err))
		return
	}
	ack()
}

// sessionSurvives dice si el evento AFIRMA que su cambio no invalida ninguna sesion del buzon:
// solo cuando trae la lista changed, bien formada, y todo lo que nombra es inofensivo. Una lista
// vacia es un cambio que no toco ningun atributo. Sin lista (un mail-directory anterior al campo),
// con una lista ilegible o con un atributo que no se reconoce se revoca, que es lo que hacia antes
// con cualquier cambio: ante la duda se cierra la sesion y el usuario vuelve a entrar.
func sessionSurvives(data map[string]any) bool {
	changed, ok := data[changedField].([]any)
	if !ok {
		return false
	}
	for _, item := range changed {
		attr, ok := item.(string)
		if !ok || !harmlessChanges[attr] {
			return false
		}
	}
	return true
}

// revokedAt es el instante que se revoca: el mas tardio entre changed_at (cuando el
// productor lo informa) y la hora del evento, mas el margen. Sin ninguno, la hora actual.
func (c *Consumer) revokedAt(evt events.Event, data map[string]any) time.Time {
	at := evt.Timestamp
	if raw, ok := data["changed_at"].(string); ok {
		if changed, err := time.Parse(time.RFC3339, raw); err == nil && changed.After(at) {
			at = changed
		}
	}
	if at.IsZero() {
		at = c.now()
	}
	return at.Add(revocationMargin)
}
