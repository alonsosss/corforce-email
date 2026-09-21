package events

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

type entregaDoble struct {
	subject string
	payload []byte
	meta    *nats.MsgMetadata
	acks    int
	terms   int
}

func (d *entregaDoble) Subject() string { return d.subject }
func (d *entregaDoble) Payload() []byte { return d.payload }
func (d *entregaDoble) Metadata() (*nats.MsgMetadata, error) {
	if d.meta == nil {
		return nil, nats.ErrMsgNoReply
	}
	return d.meta, nil
}
func (d *entregaDoble) Ack() error  { d.acks++; return nil }
func (d *entregaDoble) Term() error { d.terms++; return nil }

const (
	streamDePrueba = "MAIL_DIRECTORY"
	eventoDePrueba = `{"id":"7f1c","type":"mail.mailbox.updated","source":"mail-directory","tenant_id":"t1","data":{"username":"ana@example.com"}}`
)

// entrega es la entrega numero delivered de un mensaje con la secuencia seq en su stream.
func entrega(payload string, delivered, seq uint64) *entregaDoble {
	meta := &nats.MsgMetadata{NumDelivered: delivered, Stream: streamDePrueba}
	meta.Sequence.Stream = seq
	return &entregaDoble{subject: "mail.mailbox.updated", payload: []byte(payload), meta: meta}
}

type dlqDoble struct {
	copias []*nats.Msg
	err    error
}

func (q *dlqDoble) guardar(m *nats.Msg) error {
	if q.err != nil {
		return q.err
	}
	q.copias = append(q.copias, m)
	return nil
}

func sinConfirmar(Event, func()) {}

// abandonos devuelve el valor de cada serie de abandono de un consumidor que expone el registro
// de /metrics, por nombre, stream y motivo. Los contadores son globales: cada prueba usa su
// propio consumidor.
func abandonos(t *testing.T, consumer string) map[string]float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "events_dead_lettered_total" && mf.GetName() != "events_dead_letter_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["consumer"] == consumer {
				out[mf.GetName()+"{"+labels["stream"]+","+labels["reason"]+"}"] = m.GetCounter().GetValue()
			}
		}
	}
	return out
}

func ceros() map[string]float64 {
	return map[string]float64{
		"events_dead_lettered_total{MAIL_DIRECTORY,max_deliveries}":        0,
		"events_dead_lettered_total{MAIL_DIRECTORY,undecodable}":           0,
		"events_dead_letter_failures_total{MAIL_DIRECTORY,max_deliveries}": 0,
		"events_dead_letter_failures_total{MAIL_DIRECTORY,undecodable}":    0,
	}
}

func consumidor(name string, handler func(Event, func()), q *dlqDoble) *durableConsumer {
	return newDurableConsumer(streamDePrueba, name, handler, q.guardar, zap.NewNop())
}

// Al suscribirse, cada motivo nace a cero para el stream y el consumidor: las alertas sobre
// increase() ven asi el primer abandono.
func TestLasSeriesDeUnConsumidorNacenACero(t *testing.T) {
	consumidor("prueba-nacen", sinConfirmar, &dlqDoble{})
	if got, want := abandonos(t, "prueba-nacen"), ceros(); !maps.Equal(got, want) {
		t.Fatalf("al suscribirse: %v, se esperaba %v", got, want)
	}
}

// Las etiquetas son exactamente stream, consumer y reason, fijadas por el codigo. Ninguna se llama
// service (la pone el recolector) ni lleva el subject del mensaje.
func TestLasEtiquetasDeLosAbandonosSonAcotadas(t *testing.T) {
	consumidor("prueba-etiquetas", sinConfirmar, &dlqDoble{})
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	vistas := 0
	for _, mf := range mfs {
		if mf.GetName() != "events_dead_lettered_total" && mf.GetName() != "events_dead_letter_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			var nombres []string
			for _, l := range m.GetLabel() {
				nombres = append(nombres, l.GetName())
			}
			if !slices.Equal(nombres, []string{"consumer", "reason", "stream"}) {
				t.Fatalf("%s con etiquetas %v", mf.GetName(), nombres)
			}
		}
		vistas++
	}
	if vistas != 2 {
		t.Fatalf("familias de abandono registradas: %d, se esperaban 2", vistas)
	}
}

// Un handler que hace panic no tumba el proceso: el mensaje queda sin confirmar, se reentrega y, tras
// la ultima entrega permitida, pasa a EVENTS_DLQ como cualquier otro que no se pudo aplicar.
func TestUnPanicDelHandlerNoTumbaAlProcesoYAcabaEnLaDLQ(t *testing.T) {
	q := &dlqDoble{}
	c := consumidor("prueba-panic", func(Event, func()) { panic("evento venenoso") }, q)

	primera := entrega(eventoDePrueba, 1, 7)
	c.deliver(primera)
	if primera.acks != 0 || primera.terms != 0 || len(q.copias) != 0 {
		t.Fatalf("una entrega con panic no se confirma ni se abandona: acks=%d terms=%d copias=%d", primera.acks, primera.terms, len(q.copias))
	}

	ultima := entrega(eventoDePrueba, maxDeliverCount, 7)
	c.deliver(ultima)
	if ultima.terms != 1 || len(q.copias) != 1 {
		t.Fatalf("tras la ultima entrega debia ir a la DLQ: terms=%d copias=%d", ultima.terms, len(q.copias))
	}
}

// Tras la ultima entrega permitida sin ack, el mensaje se copia en EVENTS_DLQ con su subject y su
// cuerpo intactos y quien lo abandono en cabeceras, se termina y se cuenta.
func TestTrasLaUltimaEntregaLoGuardaEnLaDLQYLoCuenta(t *testing.T) {
	q := &dlqDoble{}
	c := consumidor("prueba-agotado", sinConfirmar, q)
	d := entrega(eventoDePrueba, maxDeliverCount, 42)
	c.deliver(d)

	if d.terms != 1 || d.acks != 0 {
		t.Fatalf("terms=%d acks=%d, se esperaba terminado y sin ack", d.terms, d.acks)
	}
	if len(q.copias) != 1 {
		t.Fatalf("copias en la DLQ: %d, se esperaba 1", len(q.copias))
	}
	m := q.copias[0]
	if m.Subject != "dlq.mail.mailbox.updated" {
		t.Fatalf("subject de la copia %q", m.Subject)
	}
	if string(m.Data) != eventoDePrueba {
		t.Fatalf("cuerpo de la copia %q", m.Data)
	}
	for k, v := range map[string]string{
		"Dlq-Stream":          streamDePrueba,
		"Dlq-Consumer":        "prueba-agotado",
		"Dlq-Reason":          "max_deliveries",
		"Dlq-Deliveries":      strconv.Itoa(maxDeliverCount),
		"Dlq-Stream-Sequence": "42",
	} {
		if got := m.Header.Get(k); got != v {
			t.Fatalf("cabecera %s = %q, se esperaba %q", k, got, v)
		}
	}
	if m.Header.Get(nats.MsgIdHdr) != "" {
		t.Fatal("la copia no debe llevar Nats-Msg-Id: EVENTS_DLQ descartaria la de otro consumidor")
	}
	want := ceros()
	want["events_dead_lettered_total{MAIL_DIRECTORY,max_deliveries}"] = 1
	if got := abandonos(t, "prueba-agotado"); !maps.Equal(got, want) {
		t.Fatalf("contadores %v, se esperaba %v", got, want)
	}
}

// Antes de la ultima entrega, un fallo solo espera la reentrega.
func TestAntesDeLaUltimaEntregaEsperaLaReentrega(t *testing.T) {
	q := &dlqDoble{}
	c := consumidor("prueba-reentrega", sinConfirmar, q)
	d := entrega(eventoDePrueba, maxDeliverCount-1, 7)
	c.deliver(d)
	if d.terms != 0 || d.acks != 0 || len(q.copias) != 0 {
		t.Fatalf("terms=%d acks=%d copias=%d, se esperaba solo la reentrega", d.terms, d.acks, len(q.copias))
	}
	if got := abandonos(t, "prueba-reentrega"); !maps.Equal(got, ceros()) {
		t.Fatalf("contadores %v, se esperaban a cero", got)
	}
}

// Un evento confirmado, aunque sea en la ultima entrega, no se abandona, y el ack sale una vez.
func TestUnEventoConfirmadoNoSeAbandona(t *testing.T) {
	q := &dlqDoble{}
	var recibido Event
	c := consumidor("prueba-confirmado", func(evt Event, ack func()) {
		recibido = evt
		ack()
		ack()
	}, q)
	d := entrega(eventoDePrueba, maxDeliverCount, 9)
	c.deliver(d)
	if recibido.ID != "7f1c" {
		t.Fatalf("el handler recibio %+v", recibido)
	}
	if d.acks != 1 || d.terms != 0 || len(q.copias) != 0 {
		t.Fatalf("acks=%d terms=%d copias=%d, se esperaba un solo ack", d.acks, d.terms, len(q.copias))
	}
	if got := abandonos(t, "prueba-confirmado"); !maps.Equal(got, ceros()) {
		t.Fatalf("contadores %v, se esperaban a cero", got)
	}
}

// Un ack que llega de otra goroutine despues del abandono no se envia: la decision ya es del
// abandono.
func TestUnAckTardioNoDeshaceElAbandono(t *testing.T) {
	q := &dlqDoble{}
	var ackTardio func()
	c := consumidor("prueba-ack-tardio", func(_ Event, ack func()) { ackTardio = ack }, q)
	d := entrega(eventoDePrueba, maxDeliverCount, 11)
	c.deliver(d)
	ackTardio()
	if d.acks != 0 || d.terms != 1 || len(q.copias) != 1 {
		t.Fatalf("acks=%d terms=%d copias=%d, se esperaba abandonado y sin ack", d.acks, d.terms, len(q.copias))
	}
}

// Sin metadatos de JetStream no se sabe cuantas entregas lleva: un evento legible sin ack espera
// la reentrega.
func TestSinMetadatosUnEventoLegibleNoSeAbandona(t *testing.T) {
	q := &dlqDoble{}
	c := consumidor("prueba-sin-metadatos", sinConfirmar, q)
	d := &entregaDoble{subject: "mail.mailbox.updated", payload: []byte(eventoDePrueba)}
	c.deliver(d)
	if d.terms != 0 || len(q.copias) != 0 {
		t.Fatalf("terms=%d copias=%d, se esperaba la reentrega", d.terms, len(q.copias))
	}
}

// Un cuerpo que no es un evento no llega al handler: se guarda en la primera entrega.
func TestUnCuerpoIlegibleVaALaDLQEnLaPrimeraEntrega(t *testing.T) {
	q := &dlqDoble{}
	llamado := false
	c := consumidor("prueba-ilegible", func(Event, func()) { llamado = true }, q)
	d := entrega("+ACK no es JSON", 1, 3)
	c.deliver(d)
	if llamado {
		t.Fatal("un cuerpo ilegible no debe llegar al handler")
	}
	if d.terms != 1 || len(q.copias) != 1 {
		t.Fatalf("terms=%d copias=%d, se esperaba guardado y terminado", d.terms, len(q.copias))
	}
	if m := q.copias[0]; m.Header.Get("Dlq-Reason") != "undecodable" || string(m.Data) != "+ACK no es JSON" {
		t.Fatalf("copia con motivo %q y cuerpo %q", m.Header.Get("Dlq-Reason"), m.Data)
	}
	want := ceros()
	want["events_dead_lettered_total{MAIL_DIRECTORY,undecodable}"] = 1
	if got := abandonos(t, "prueba-ilegible"); !maps.Equal(got, want) {
		t.Fatalf("contadores %v, se esperaba %v", got, want)
	}
}

// Si la copia falla sin entregas por delante, el mensaje se termina igual (JetStream ya no lo
// reentregaria) y se cuenta como abandonado sin copia. Con entregas por delante no se termina: la
// siguiente reintenta la copia.
func TestSinCopiaEnLaDLQ(t *testing.T) {
	caida := errors.New("jetstream no responde")

	t.Run("agotado", func(t *testing.T) {
		q := &dlqDoble{err: caida}
		c := consumidor("prueba-sin-copia", sinConfirmar, q)
		d := entrega(eventoDePrueba, maxDeliverCount, 5)
		c.deliver(d)
		if d.terms != 1 {
			t.Fatalf("terms=%d, se esperaba terminado", d.terms)
		}
		want := ceros()
		want["events_dead_letter_failures_total{MAIL_DIRECTORY,max_deliveries}"] = 1
		if got := abandonos(t, "prueba-sin-copia"); !maps.Equal(got, want) {
			t.Fatalf("contadores %v, se esperaba %v", got, want)
		}
	})

	t.Run("ilegible con entregas por delante", func(t *testing.T) {
		q := &dlqDoble{err: caida}
		c := consumidor("prueba-ilegible-sin-copia", sinConfirmar, q)
		d := entrega("{", 1, 6)
		c.deliver(d)
		if d.terms != 0 {
			t.Fatalf("terms=%d, la siguiente entrega debe reintentar la copia", d.terms)
		}
		if got := abandonos(t, "prueba-ilegible-sin-copia"); !maps.Equal(got, ceros()) {
			t.Fatalf("contadores %v, se esperaban a cero", got)
		}

		ultima := entrega("{", maxDeliverCount, 6)
		c.deliver(ultima)
		if ultima.terms != 1 {
			t.Fatalf("terms=%d en la ultima entrega, se esperaba terminado", ultima.terms)
		}
		want := ceros()
		want["events_dead_letter_failures_total{MAIL_DIRECTORY,undecodable}"] = 1
		if got := abandonos(t, "prueba-ilegible-sin-copia"); !maps.Equal(got, want) {
			t.Fatalf("contadores %v, se esperaba %v", got, want)
		}
	})
}

type profundidadDoble struct {
	n   uint64
	err error
}

func (p *profundidadDoble) dlqMessages() (uint64, error) { return p.n, p.err }

// events_dlq_messages solo sale de un proceso con consumidor durable y con respuesta de JetStream.
func TestLaProfundidadDeLaDLQSoloConRespuestaDeJetStream(t *testing.T) {
	col := newDLQDepthCollector()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(col)
	profundidad := func() (float64, bool) {
		t.Helper()
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() == "events_dlq_messages" && len(mf.GetMetric()) == 1 {
				return mf.GetMetric()[0].GetGauge().GetValue(), true
			}
		}
		return 0, false
	}

	if _, ok := profundidad(); ok {
		t.Fatal("sin consumidor durable no hay serie")
	}
	fuente := &profundidadDoble{n: 3}
	col.bind(fuente)
	if v, ok := profundidad(); !ok || v != 3 {
		t.Fatalf("profundidad %v (%v), se esperaba 3", v, ok)
	}
	fuente.err = nats.ErrDisconnected
	if v, ok := profundidad(); ok {
		t.Fatalf("sin respuesta de JetStream se publico %v", v)
	}
	fuente.err, fuente.n = nil, 0
	if v, ok := profundidad(); !ok || v != 0 {
		t.Fatalf("con la DLQ vacia o sin crear: %v (%v), se esperaba 0", v, ok)
	}
	col.unbind(&profundidadDoble{})
	if _, ok := profundidad(); !ok {
		t.Fatal("desatar otra fuente no debe quitar la serie")
	}
	col.unbind(fuente)
	if _, ok := profundidad(); ok {
		t.Fatal("tras cerrar el bus no hay serie")
	}
}
