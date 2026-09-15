package events

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Cola de mensajes muertos de los consumidores durables. Un mensaje que su consumidor abandona
// (agoto sus entregas sin confirmarse, o no es un evento legible) se copia en EVENTS_DLQ bajo
// dlq.<subject del mensaje>, con el cuerpo intacto y en cabeceras quien lo abandono y por que, y
// deja de reentregarse. Cada uno es un efecto de negocio que no se aplico y que nada repite solo:
// lo cuentan las metricas de abajo, lo avisan las alertas del grupo eventos de
// ops/observability/prometheus/rules/plataforma.yml y se reproduce como dice
// docs/Operacion_Despliegue.md, seccion 10.

const (
	dlqStreamName    = "EVENTS_DLQ"
	dlqSubjectPrefix = "dlq."
	dlqMaxAge        = 30 * 24 * time.Hour
	// dlqDepthTimeout acota la consulta a JetStream que hace cada recoleccion de /metrics.
	dlqDepthTimeout = 2 * time.Second
)

// Cabeceras de la copia en EVENTS_DLQ.
const (
	headerDLQStream         = "Dlq-Stream"
	headerDLQConsumer       = "Dlq-Consumer"
	headerDLQReason         = "Dlq-Reason"
	headerDLQDeliveries     = "Dlq-Deliveries"
	headerDLQStreamSequence = "Dlq-Stream-Sequence"
)

// Motivos de abandono: etiqueta reason y cabecera Dlq-Reason.
const (
	reasonMaxDeliveries = "max_deliveries"
	reasonUndecodable   = "undecodable"
)

var abandonReasons = []string{reasonMaxDeliveries, reasonUndecodable}

// Etiquetas acotadas por el codigo: el stream de origen y el nombre del consumidor durable, los
// dos fijos al suscribirse. Un consumidor tiene un solo subject de filtro, asi que el subject no
// anade nada; el concreto del mensaje queda en la copia, nunca en una etiqueta. Ninguna se llama
// service, que la pone el recolector.
var (
	deadLettered = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "events_dead_lettered_total",
		Help: "Mensajes que un consumidor durable abandono y guardo en EVENTS_DLQ, por stream de origen, consumidor y motivo (max_deliveries: agoto sus entregas sin confirmarse; undecodable: no es un evento legible). Cada uno es un efecto de negocio que no se aplico.",
	}, []string{"stream", "consumer", "reason"})
	deadLetterFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "events_dead_letter_failures_total",
		Help: "Mensajes que un consumidor durable abandono sin poder guardarlos en EVENTS_DLQ: solo quedan en su stream de origen hasta que caduquen.",
	}, []string{"stream", "consumer", "reason"})
	dlqDepth = newDLQDepthCollector()
)

func init() { prometheus.MustRegister(deadLettered, deadLetterFailures, dlqDepth) }

// delivery es lo que el consumidor durable usa de un mensaje de JetStream.
type delivery interface {
	Subject() string
	Payload() []byte
	Metadata() (*nats.MsgMetadata, error)
	Ack() error
	Term() error
}

type natsDelivery struct{ msg *nats.Msg }

func (d natsDelivery) Subject() string                      { return d.msg.Subject }
func (d natsDelivery) Payload() []byte                      { return d.msg.Data }
func (d natsDelivery) Metadata() (*nats.MsgMetadata, error) { return d.msg.Metadata() }
func (d natsDelivery) Ack() error                           { return d.msg.Ack() }
func (d natsDelivery) Term() error                          { return d.msg.Term() }

// durableConsumer entrega cada mensaje de un consumidor durable a su handler y decide cuando
// abandonarlo.
type durableConsumer struct {
	stream     string
	name       string
	maxDeliver uint64
	handler    func(Event, func())
	deadLetter func(*nats.Msg) error
	logger     *zap.Logger
}

func newDurableConsumer(stream, name string, handler func(Event, func()), deadLetter func(*nats.Msg) error, logger *zap.Logger) *durableConsumer {
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, reason := range abandonReasons {
		deadLettered.WithLabelValues(stream, name, reason)
		deadLetterFailures.WithLabelValues(stream, name, reason)
	}
	return &durableConsumer{stream: stream, name: name, maxDeliver: maxDeliverCount, handler: handler, deadLetter: deadLetter, logger: logger}
}

func (c *durableConsumer) deliver(d delivery) {
	var evt Event
	if err := json.Unmarshal(d.Payload(), &evt); err != nil {
		// Reentregar un cuerpo ilegible no lo arregla.
		meta, _ := d.Metadata()
		c.abandon(d, meta, reasonUndecodable, err)
		return
	}
	// El ack puede llegar desde OTRA goroutine: hay handlers que resuelven el pool
	// del tenant en segundo plano y confirman al terminar. Por eso el flag es
	// atomico y el abandono solo se decide si el handler no acko NI va a
	// ackear: `acked` se consulta con Swap para que la decision sea de uno u otro,
	// nunca de los dos.
	var acked atomic.Bool
	c.handler(evt, func() {
		if !acked.Swap(true) {
			_ = d.Ack()
		}
	})
	if acked.Load() {
		return
	}
	// Sin ack se reentrega; tras la ultima entrega permitida JetStream ya no lo haria.
	meta, err := d.Metadata()
	if err != nil || meta.NumDelivered < c.maxDeliver || acked.Swap(true) {
		return
	}
	c.abandon(d, meta, reasonMaxDeliveries, nil)
}

// abandon copia el mensaje en EVENTS_DLQ y lo termina. Si la copia falla y le quedan entregas, no
// lo termina: la siguiente reintenta la copia. Sin entregas lo termina igual y solo queda en su
// stream de origen hasta que caduque; el registro da su secuencia para recuperarlo.
func (c *durableConsumer) abandon(d delivery, meta *nats.MsgMetadata, reason string, cause error) {
	fields := []zap.Field{
		zap.String("stream", c.stream),
		zap.String("consumer", c.name),
		zap.String("subject", d.Subject()),
		zap.String("reason", reason),
	}
	if meta != nil {
		fields = append(fields, zap.Uint64("stream_seq", meta.Sequence.Stream), zap.Uint64("deliveries", meta.NumDelivered))
	}
	if cause != nil {
		fields = append(fields, zap.Error(cause))
	}
	err := c.deadLetter(dlqCopy(d, meta, c.stream, c.name, reason))
	if err == nil {
		deadLettered.WithLabelValues(c.stream, c.name, reason).Inc()
		c.logger.Error("evento abandonado y guardado en EVENTS_DLQ", fields...)
		_ = d.Term()
		return
	}
	fields = append(fields, zap.NamedError("dlq_error", err))
	if meta != nil && meta.NumDelivered < c.maxDeliver {
		c.logger.Warn("copia en EVENTS_DLQ fallida; se reintenta en la siguiente entrega", fields...)
		return
	}
	deadLetterFailures.WithLabelValues(c.stream, c.name, reason).Inc()
	c.logger.Error("evento abandonado sin copia en EVENTS_DLQ: solo queda en su stream de origen", fields...)
	_ = d.Term()
}

// dlqCopy no hereda las cabeceras del mensaje: con su Nats-Msg-Id, EVENTS_DLQ descartaria como
// duplicada la copia de un segundo consumidor que abandone el mismo evento.
func dlqCopy(d delivery, meta *nats.MsgMetadata, stream, consumer, reason string) *nats.Msg {
	m := nats.NewMsg(dlqSubjectPrefix + d.Subject())
	m.Data = d.Payload()
	m.Header.Set(headerDLQStream, stream)
	m.Header.Set(headerDLQConsumer, consumer)
	m.Header.Set(headerDLQReason, reason)
	if meta != nil {
		m.Header.Set(headerDLQDeliveries, strconv.FormatUint(meta.NumDelivered, 10))
		m.Header.Set(headerDLQStreamSequence, strconv.FormatUint(meta.Sequence.Stream, 10))
	}
	return m
}

// deadLetter guarda la copia en EVENTS_DLQ, que crea el primero que la necesita.
func (b *Bus) deadLetter(m *nats.Msg) error {
	js, err := b.conn.JetStream()
	if err != nil {
		return err
	}
	if _, err := js.StreamInfo(dlqStreamName); errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     dlqStreamName,
			Subjects: []string{"dlq.>"},
			Storage:  nats.FileStorage,
			MaxAge:   dlqMaxAge,
			Replicas: 1,
		})
		// Otra instancia pudo crearla a la vez: la copia se intenta igual.
		if err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return err
		}
	}
	_, err = js.PublishMsg(m)
	return err
}

// dlqMessages cuenta los mensajes de EVENTS_DLQ; sin crear todavia, ninguno.
func (b *Bus) dlqMessages() (uint64, error) {
	if !b.conn.IsConnected() {
		return 0, nats.ErrDisconnected
	}
	js, err := b.conn.JetStream()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dlqDepthTimeout)
	defer cancel()
	info, err := js.StreamInfo(dlqStreamName, nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.State.Msgs, nil
}

type dlqCounter interface {
	dlqMessages() (uint64, error)
}

// dlqDepthCollector publica events_dlq_messages preguntando a JetStream en cada recoleccion. Lo
// publica todo proceso con un consumidor durable, con el mismo valor en todos. Sin respuesta de
// JetStream no publica la serie: una cifra vieja pasaria por buena.
type dlqDepthCollector struct {
	desc   *prometheus.Desc
	mu     sync.RWMutex
	source dlqCounter
}

func newDLQDepthCollector() *dlqDepthCollector {
	return &dlqDepthCollector{desc: prometheus.NewDesc(
		"events_dlq_messages",
		"Mensajes que guarda EVENTS_DLQ, pendientes de reproducir o descartar. Cada proceso con un consumidor durable publica el mismo valor.",
		nil, nil,
	)}
}

func (c *dlqDepthCollector) bind(source dlqCounter) {
	c.mu.Lock()
	c.source = source
	c.mu.Unlock()
}

func (c *dlqDepthCollector) unbind(source dlqCounter) {
	c.mu.Lock()
	if c.source == source {
		c.source = nil
	}
	c.mu.Unlock()
}

func (c *dlqDepthCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *dlqDepthCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	source := c.source
	c.mu.RUnlock()
	if source == nil {
		return
	}
	n, err := source.dlqMessages()
	if err != nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n))
}
