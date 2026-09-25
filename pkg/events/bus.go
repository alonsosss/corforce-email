package events

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/observability"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// streamMaxBytes acota el disco de cada stream de JetStream. La retencion por tiempo sola no protege
// el disco: un productor desbocado o un consumidor caido llenaria el volumen del servidor (donde
// tambien viven la base y los buzones) mucho antes de los 7 dias. Al llegar al tope se descartan los
// mensajes mas viejos; 1 GiB son millones de eventos de unos cientos de bytes, muy por encima de la
// carga normal, asi que llegar aqui es un incidente y no el funcionamiento habitual.
const streamMaxBytes = 1 << 30

type Event struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	Source    string      `json:"source"`
	TenantID  string      `json:"tenant_id"`
	UserID    string      `json:"user_id,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// readinessName es la dependencia del bus en /readyz.
const readinessName = "nats"

type Bus struct {
	conn   *nats.Conn
	logger *zap.Logger
}

func NewBus(url string, logger *zap.Logger) (*Bus, error) {
	// Sin tope de reconexiones: con un tope, tras una caida larga de NATS la conexion se
	// cerraba para siempre y dejaban de llegar eventos como la revocacion de sesiones del
	// webmail. Las suscripciones se restauran solas al volver a conectar.
	conn, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warn("event bus disconnected", zap.Error(err))
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			logger.Info("event bus reconnected", zap.String("url", c.ConnectedUrlRedacted()))
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	logger.Info("event bus connected", zap.String("url", url))
	observability.RegisterReadiness(readinessName, func(context.Context) error {
		if !conn.IsConnected() {
			return nats.ErrDisconnected
		}
		return nil
	})
	return &Bus{conn: conn, logger: logger}, nil
}

func (b *Bus) Publish(subject string, evt Event) error {
	if evt.ID == "" {
		evt.ID = uuid.New().String()
	}
	evt.Timestamp = time.Now().UTC()

	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return b.conn.Publish(subject, data)
}

// streamConfig es la configuracion de todo stream de aplicacion: disco, retencion por tiempo Y por
// tamano, y deduplicacion de 5 minutos.
func streamConfig(name string, subjects []string, maxAge time.Duration) *nats.StreamConfig {
	return &nats.StreamConfig{
		Name:       name,
		Subjects:   subjects,
		Storage:    nats.FileStorage,
		MaxAge:     maxAge,
		MaxBytes:   streamMaxBytes,
		Duplicates: 5 * time.Minute,
		Replicas:   1,
	}
}

// EnsureStream creates or verifies a JetStream stream backed by disk storage.
// Safe to call multiple times — idempotent.
//
// Si el stream ya existe, agrega los subjects que falten (union). Sin esto, un
// servicio que suma un evento nuevo a un stream ya creado nunca lo recibiria
// ("no stream matches subject") hasta recrear el stream a mano. La union no
// quita subjects ajenos a proposito: varios servicios comparten un mismo stream
// (p. ej. CRM_INBOUND) y reemplazar la lista los haria pisarse entre si.
func (b *Bus) EnsureStream(name string, subjects []string) error {
	return b.EnsureStreamWithMaxAge(name, subjects, 7*24*time.Hour)
}

// EnsureStreamWithMaxAge es EnsureStream con la retencion explicita. La retencion
// tambien se reconcilia sobre un stream existente: cuando dos servicios (o dos
// lenguajes) la pedian distinta, ganaba el que arrancara primero y nadie lo veia.
func (b *Bus) EnsureStreamWithMaxAge(name string, subjects []string, maxAge time.Duration) error {
	js, err := b.conn.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}
	cfg := streamConfig(name, subjects, maxAge)

	// Varios servicios de un mismo cluster aseguran el MISMO stream al arrancar,
	// y arrancan a la vez tras cada despliegue. Eso produce dos carreras que no
	// fallan de forma visible:
	//
	//   1. Dos AddStream simultaneos sobre un stream que no existia: uno pierde
	//      con "already in use" aunque su intencion era legitima.
	//   2. Dos UpdateStream simultaneos: cada uno calcula la union sobre la foto
	//      que leyo, y el ultimo PISA el subject que el otro acababa de anadir.
	//
	// La segunda es la peor, porque deja a un consumidor sin su subject sin que
	// ningun servicio falle: los mensajes publicados a ese subject se rechazan o
	// nadie los recibe, segun el lado que perdio. Por eso esto no se hace una
	// vez: se VERIFICA que los subjects pedidos quedaron en el stream y se
	// reintenta la union hasta que sea cierto.
	var ultimo error
	for intento := 0; intento < 6; intento++ {
		if intento > 0 {
			time.Sleep(time.Duration(intento) * 100 * time.Millisecond)
		}
		info, err := js.StreamInfo(name)
		if err == nats.ErrStreamNotFound {
			if _, err = js.AddStream(cfg); err == nil {
				return nil
			}
			// Otra instancia gano la creacion: se relee y se pasa a la union.
			ultimo = err
			continue
		}
		if err != nil {
			ultimo = err
			continue
		}

		merged := append([]string(nil), info.Config.Subjects...)
		added := false
		for _, want := range subjects {
			found := false
			for _, have := range merged {
				if have == want {
					found = true
					break
				}
			}
			if !found {
				merged = append(merged, want)
				added = true
			}
		}
		if !added && info.Config.MaxAge == maxAge && info.Config.MaxBytes == streamMaxBytes {
			return nil
		}
		updated := info.Config
		updated.Subjects = merged
		updated.MaxAge = maxAge
		updated.MaxBytes = streamMaxBytes
		if _, err = js.UpdateStream(&updated); err != nil {
			ultimo = err
			continue
		}
		// La actualizacion pudo cruzarse con otra: no se da por buena hasta
		// releer y comprobar que lo pedido sigue ahi.
	}
	if ultimo == nil {
		ultimo = fmt.Errorf("no se pudo asegurar el stream %s con sus subjects", name)
	}
	return ultimo
}

// PublishPersistent publishes to a JetStream stream with deduplication via message ID.
// Returns an error if the publish is not acknowledged by the server — callers should
// treat this as a signal to return HTTP 5xx so the upstream webhook provider retries.
func (b *Bus) PublishPersistent(subject string, evt Event) error {
	js, err := b.conn.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}
	if evt.ID == "" {
		evt.ID = uuid.New().String()
	}
	evt.Timestamp = time.Now().UTC()
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	// MsgId enables server-side deduplication within the Duplicates window.
	_, err = js.Publish(subject, data, nats.MsgId(evt.ID))
	return err
}

// maxDeliverCount must match the MaxDeliver option below. When a message reaches
// it without being acked, JetStream would silently drop it; we dead-letter it first.
const maxDeliverCount = 20

// ackWaitDuration is the time JetStream waits for an ack before redelivering. It
// must exceed the slowest handler (inbound media download budget ~60s) so a
// message is not redelivered while it is still being processed.
const ackWaitDuration = 90 * time.Second

// DurableQueueSubscribe binds a durable JetStream push consumer.
// handler receives the event and an ack function — ack MUST be called after
// successful processing; on error the message will be redelivered. A message that
// exhausts its redelivery budget, or is not a readable event, is dead-lettered
// (copied to the EVENTS_DLQ stream and counted, see deadletter.go) instead of being
// silently discarded.
//
// El consumidor se crea explicitamente (AddConsumer) y la suscripcion se ata con
// Bind. Antes lo creaba js.Subscribe de forma implicita, y en ese modo nats.go
// BORRA el consumidor durable al hacer Unsubscribe: cada apagado de un worker
// destruia su durable con todo su estado de acks, y el siguiente arranque
// re-reproducia el stream completo. La idempotencia de los handlers lo
// disimulaba, pero era trabajo repetido en cada deploy y un riesgo real de
// duplicados cuando una clave de idempotencia cambia entre versiones.
func (b *Bus) DurableQueueSubscribe(subject, durable string, handler func(Event, func())) (*nats.Subscription, error) {
	return b.durableSubscribe(subject, durable, nats.DeliverAllPolicy, handler)
}

// DurableQueueSubscribeNew es DurableQueueSubscribe para un consumidor que reemplaza a una
// suscripcion de nucleo que ya registraba el mismo flujo: al CREAR el durable solo recibe lo
// publicado desde ese momento, en vez de reproducir todo lo que el stream retiene (lo que la
// suscripcion anterior ya trato y volveria a aplicarse). Un durable que ya existe conserva su
// posicion y esta politica no le afecta.
func (b *Bus) DurableQueueSubscribeNew(subject, durable string, handler func(Event, func())) (*nats.Subscription, error) {
	return b.durableSubscribe(subject, durable, nats.DeliverNewPolicy, handler)
}

func (b *Bus) durableSubscribe(subject, durable string, deliver nats.DeliverPolicy, handler func(Event, func())) (*nats.Subscription, error) {
	js, err := b.conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}
	stream, err := js.StreamNameBySubject(subject)
	if err != nil {
		return nil, fmt.Errorf("stream for %s: %w", subject, err)
	}
	desired := &nats.ConsumerConfig{
		Durable:        durable,
		DeliverSubject: "deliver." + durable,
		FilterSubject:  subject,
		AckPolicy:      nats.AckExplicitPolicy,
		AckWait:        ackWaitDuration,
		MaxDeliver:     maxDeliverCount,
		DeliverPolicy:  deliver,
	}
	if info, ierr := js.ConsumerInfo(stream, durable); ierr == nil && info != nil {
		if info.Config.DeliverSubject != desired.DeliverSubject {
			// Consumidor legado creado implicitamente por js.Subscribe (deliver
			// subject efimero): migrarlo una unica vez al administrado. Se pierde
			// su ack floor, como ya ocurria en cada deploy; desde aqui persiste.
			if derr := js.DeleteConsumer(stream, durable); derr != nil {
				b.logger.Warn("delete legacy consumer failed", zap.String("durable", durable), zap.Error(derr))
			}
			if _, aerr := js.AddConsumer(stream, desired); aerr != nil {
				return nil, fmt.Errorf("migrate consumer %s: %w", durable, aerr)
			}
			b.logger.Info("legacy consumer migrated to managed", zap.String("durable", durable))
		} else if info.Config.AckWait != ackWaitDuration || info.Config.MaxDeliver != maxDeliverCount {
			cfg := info.Config
			cfg.AckWait = ackWaitDuration
			cfg.MaxDeliver = maxDeliverCount
			if _, uerr := js.UpdateConsumer(stream, &cfg); uerr != nil {
				b.logger.Warn("update consumer config failed", zap.String("durable", durable), zap.Error(uerr))
			}
		}
	} else {
		if _, aerr := js.AddConsumer(stream, desired); aerr != nil {
			return nil, fmt.Errorf("create consumer %s: %w", durable, aerr)
		}
	}
	consumer := newDurableConsumer(stream, durable, handler, b.deadLetter, b.logger)
	dlqDepth.bind(b)
	return js.Subscribe(subject, func(msg *nats.Msg) { consumer.deliver(natsDelivery{msg: msg}) },
		nats.Bind(stream, durable),
		nats.ManualAck(),
	)
}

// isSystemSubject identifica los subjects internos de NATS/JetStream ($JS.ACK.*,
// $SYS.*, ...). Un suscriptor comodin como "*.>" TAMBIEN los recibe: el token "*" casa
// con "$JS", asi que cada acuse de JetStream llegaba a los servicios que escuchan todo
// el bus y se registraba como error de deserializacion con stacktrace ("+ACK" no es
// JSON). Nunca son eventos de aplicacion: se descartan antes de intentar leerlos.
func isSystemSubject(subject string) bool {
	return strings.HasPrefix(subject, "$")
}

// logUnmarshalFailure deja el subject REAL del mensaje, no el patron de suscripcion:
// un suscriptor comodin ("*.>") reportaba siempre "*.>" y no habia forma de saber que
// publicador emitia un payload ilegible. Se incluye un prefijo del cuerpo (acotado) para
// identificarlo sin volcar datos de negocio al log.
func (b *Bus) logUnmarshalFailure(pattern string, msg *nats.Msg, err error) {
	const previewLimit = 48
	preview := msg.Data
	if len(preview) > previewLimit {
		preview = preview[:previewLimit]
	}
	b.logger.Error("unmarshal event",
		zap.Error(err),
		zap.String("pattern", pattern),
		zap.String("subject", msg.Subject),
		zap.Int("bytes", len(msg.Data)),
		zap.ByteString("preview", preview))
}

func (b *Bus) Subscribe(subject string, handler func(Event)) (*nats.Subscription, error) {
	return b.conn.Subscribe(subject, func(msg *nats.Msg) {
		if isSystemSubject(msg.Subject) {
			return
		}
		var evt Event
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			b.logUnmarshalFailure(subject, msg, err)
			return
		}
		handler(evt)
	})
}

func (b *Bus) QueueSubscribe(subject, queue string, handler func(Event)) (*nats.Subscription, error) {
	return b.conn.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		if isSystemSubject(msg.Subject) {
			return
		}
		var evt Event
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			b.logUnmarshalFailure(subject, msg, err)
			return
		}
		handler(evt)
	})
}

func (b *Bus) Close() {
	dlqDepth.unbind(b)
	observability.UnregisterReadiness(readinessName)
	b.conn.Close()
	b.logger.Info("event bus closed")
}
