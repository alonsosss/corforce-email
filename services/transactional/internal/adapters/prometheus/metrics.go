// Package prometheus expone las metricas propias de transactional en el registro que sirve
// pkg/server en /metrics.
package prometheus

import (
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.Metrics. Las etiquetas toman valores de conjuntos cerrados: la clase,
// el resultado del intento, el tipo de evento normalizado y el motivo de rechazo.
type Metrics struct {
	sendAttempts      *prometheus.CounterVec
	sesEvents         *prometheus.CounterVec
	sesRejected       *prometheus.CounterVec
	sendingEnabled    prometheus.Gauge
	productionAccess  prometheus.Gauge
	max24h            prometheus.Gauge
	sent24h           prometheus.Gauge
	maxRate           prometheus.Gauge
	bounceRate        prometheus.Gauge
	complaintRate     prometheus.Gauge
	reputationKnown   *prometheus.GaugeVec
	lastSuccess       prometheus.Gauge
	checkFailures     prometheus.Counter
	marketingDeferred prometheus.Counter
	relayMessages     *prometheus.CounterVec
	now               func() time.Time
}

func New() *Metrics {
	gauge := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	}
	m := &Metrics{
		sendAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transactional_send_attempts_total",
			Help: "Intentos de entrega a SES, por clase y resultado (sent, throttled, paused, transient, permanent).",
		}, []string{"class", "result"}),
		sesEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transactional_ses_events_total",
			Help: "Eventos de SES autenticos recibidos por SNS, por tipo.",
		}, []string{"type"}),
		sesRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transactional_ses_events_rejected_total",
			Help: "Notificaciones rechazadas o ignoradas en la ruta de eventos de SES, por motivo.",
		}, []string{"reason"}),
		sendingEnabled:   gauge("transactional_ses_account_sending_enabled", "1 si la cuenta de SES tiene el envio habilitado."),
		productionAccess: gauge("transactional_ses_account_production_access", "1 si la cuenta de SES salio del modo de pruebas."),
		max24h:           gauge("transactional_ses_account_max_24h_send", "Cuota de envios en 24 horas de la cuenta de SES."),
		sent24h:          gauge("transactional_ses_account_sent_last_24h", "Envios de las ultimas 24 horas segun SES."),
		maxRate:          gauge("transactional_ses_account_max_send_rate", "Envios por segundo que admite la cuenta de SES."),
		bounceRate:       gauge("transactional_ses_reputation_bounce_rate", "Tasa de rebotes de la cuenta (Reputation.BounceRate, de 0 a 1)."),
		complaintRate:    gauge("transactional_ses_reputation_complaint_rate", "Tasa de quejas de la cuenta (Reputation.ComplaintRate, de 0 a 1)."),
		reputationKnown: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "transactional_ses_reputation_known",
			Help: "1 si CloudWatch tenia dato de esa tasa en la ultima lectura; sin dato, la tasa conserva el ultimo valor.",
		}, []string{"metric"}),
		lastSuccess: gauge("transactional_ses_account_last_success_timestamp_seconds", "Momento de la ultima lectura correcta del estado de la cuenta de SES."),
		checkFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "transactional_ses_account_check_failures_total",
			Help: "Lecturas fallidas del estado de la cuenta de SES.",
		}),
		marketingDeferred: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "transactional_marketing_quota_deferred_total",
			Help: "Mensajes de marketing aplazados porque la cuenta de SES entro en la reserva de cuota del transaccional.",
		}),
		relayMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transactional_relay_messages_total",
			Help: "Mensajes aceptados por el relay SMTP, por cuenta (plataforma, la clave global compartida; empresa, una cuenta propia) y clase (aviso o masivo, segun sus cabeceras).",
		}, []string{"account", "class"}),
		now: time.Now,
	}
	prometheus.MustRegister(m.sendAttempts, m.sesEvents, m.sesRejected, m.sendingEnabled, m.productionAccess,
		m.max24h, m.sent24h, m.maxRate, m.bounceRate, m.complaintRate, m.reputationKnown, m.lastSuccess, m.checkFailures, m.marketingDeferred, m.relayMessages)
	return m
}

func (m *Metrics) SendAttempt(class, result string) {
	m.sendAttempts.WithLabelValues(domain.ClassOrDefault(class), result).Inc()
}

func (m *Metrics) SESEvent(eventType string) { m.sesEvents.WithLabelValues(eventType).Inc() }

func (m *Metrics) SESEventRejected(reason string) { m.sesRejected.WithLabelValues(reason).Inc() }

func (m *Metrics) SESAccount(s domain.SESAccountStatus) {
	m.sendingEnabled.Set(boolValue(s.SendingEnabled))
	m.productionAccess.Set(boolValue(s.ProductionAccess))
	m.max24h.Set(s.Max24HourSend)
	m.sent24h.Set(s.SentLast24Hours)
	m.maxRate.Set(s.MaxSendRate)
	setRate(m.bounceRate, m.reputationKnown.WithLabelValues("bounce"), s.BounceRate)
	setRate(m.complaintRate, m.reputationKnown.WithLabelValues("complaint"), s.ComplaintRate)
	m.lastSuccess.Set(float64(m.now().Unix()))
}

func (m *Metrics) SESAccountCheckFailed() { m.checkFailures.Inc() }

func (m *Metrics) MarketingDeferred() { m.marketingDeferred.Inc() }

func (m *Metrics) RelayMessage(account, class string) {
	m.relayMessages.WithLabelValues(account, class).Inc()
}

func setRate(g prometheus.Gauge, known prometheus.Gauge, v *float64) {
	if v == nil {
		known.Set(0)
		return
	}
	g.Set(*v)
	known.Set(1)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
