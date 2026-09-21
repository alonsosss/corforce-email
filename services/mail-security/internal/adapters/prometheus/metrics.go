// Package prometheus expone las metricas propias de mail-security en el registro que sirve
// pkg/server en /metrics.
package prometheus

import (
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.DKIMReconcileMetrics y ports.SessionRevocationMetrics. Las alertas que
// las leen estan en los grupos claves-dkim y revocacion-en-dovecot de
// ops/observability/prometheus/rules/plataforma.yml.
type Metrics struct {
	dkimRemovals       *prometheus.CounterVec
	dkimUnresolved     prometheus.Counter
	dkimLastSuccess    prometheus.Gauge
	dovecotRevocations *prometheus.CounterVec
	dovecotFailures    *prometheus.CounterVec
	quarantine         *prometheus.CounterVec
	queueMessages      *prometheus.GaugeVec
	queueOldest        prometheus.Gauge
	queuePollSuccess   prometheus.Gauge
	queuePollFailures  prometheus.Counter
}

func New() *Metrics {
	m := &Metrics{
		dkimRemovals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_dkim_reconcile_removals_total",
			Help: "Dominios a los que el repaso retiro las claves DKIM de los motores, por motivo (not_served: no esta activo en el directorio de la celda; tenant_gone: organization ya no conoce su empresa).",
		}, []string{"reason"}),
		dkimUnresolved: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mail_security_dkim_reconcile_unresolved_total",
			Help: "Dominios cuyas claves DKIM el repaso conservo porque organization no dio su empresa.",
		}),
		dkimLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_security_dkim_reconcile_last_success_timestamp_seconds",
			Help: "Instante Unix de la ultima pasada completa del repaso de claves DKIM en esta replica; 0 si no ha completado ninguna desde que arranco.",
		}),
		dovecotRevocations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_dovecot_revocations_total",
			Help: "Buzones cuya credencial se retiro de Dovecot con un evento del directorio, por accion (flush: cache de autenticacion vaciada; kick: vaciada y sesiones abiertas cerradas).",
		}, []string{"action"}),
		dovecotFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_dovecot_revocation_failures_total",
			Help: "Revocaciones en Dovecot que fallaron y esperan la reentrega del evento, por motivo (unreachable, rejected, command, directory).",
		}, []string{"reason"}),
		quarantine: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_quarantine_messages_total",
			Help: "Mensajes de la cuarentena de la celda por lo que paso con ellos (stored: retenidos; released: liberados por su dueno o por enlace, los falsos positivos; discarded: descartados; learned_spam: usados para entrenar el clasificador como spam). released frente a stored mide los falsos positivos del antispam.",
		}, []string{"outcome"}),
		queueMessages: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "mail_security_postfix_queue_messages",
			Help: "Mensajes en la cola de Postfix de la celda, por cola (incoming, active, deferred, hold, corrupt), en la ultima consulta que salio bien.",
		}, []string{"queue"}),
		queueOldest: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_security_postfix_queue_oldest_arrival_timestamp_seconds",
			Help: "Instante Unix de llegada del mensaje mas antiguo de la cola de Postfix; 0 con la cola vacia.",
		}),
		queuePollSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_security_postfix_queue_last_poll_success_timestamp_seconds",
			Help: "Instante Unix de la ultima consulta correcta a la cola de Postfix; 0 si aun no ha habido ninguna (o el gestor de cola esta desactivado).",
		}),
		queuePollFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mail_security_postfix_queue_poll_failures_total",
			Help: "Consultas a la cola de Postfix que fallaron (agente caido, clave o certificado rechazados).",
		}),
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, reason := range domain.DKIMRemovalReasons() {
		m.dkimRemovals.WithLabelValues(string(reason))
	}
	for _, action := range domain.SessionActions() {
		m.dovecotRevocations.WithLabelValues(string(action))
	}
	for _, reason := range domain.SessionRevocationFailures() {
		m.dovecotFailures.WithLabelValues(string(reason))
	}
	for _, outcome := range []string{"stored", "released", "discarded", "learned_spam"} {
		m.quarantine.WithLabelValues(outcome)
	}
	for _, queue := range domain.QueueNames() {
		m.queueMessages.WithLabelValues(queue)
	}
	prometheus.MustRegister(m.dkimRemovals, m.dkimUnresolved, m.dkimLastSuccess, m.dovecotRevocations, m.dovecotFailures,
		m.quarantine, m.queueMessages, m.queueOldest, m.queuePollSuccess, m.queuePollFailures)
	return m
}

func (m *Metrics) SessionsRevoked(action domain.SessionAction) {
	m.dovecotRevocations.WithLabelValues(string(action)).Inc()
}

func (m *Metrics) SessionRevocationFailed(reason domain.SessionRevocationFailure) {
	m.dovecotFailures.WithLabelValues(string(reason)).Inc()
}

func (m *Metrics) DKIMKeysRemoved(reason domain.DKIMRemovalReason, domains int) {
	if domains > 0 {
		m.dkimRemovals.WithLabelValues(string(reason)).Add(float64(domains))
	}
}

func (m *Metrics) DKIMUnresolved(domains int) {
	if domains > 0 {
		m.dkimUnresolved.Add(float64(domains))
	}
}

func (m *Metrics) DKIMReconciled(at time.Time) {
	m.dkimLastSuccess.Set(float64(at.UnixNano()) / 1e9)
}

// QueueObserved anota una consulta correcta: las colas que no aparecen valen cero, no conservan el valor anterior.
func (m *Metrics) QueueObserved(counts map[string]int, oldestArrival time.Time) {
	for _, queue := range domain.QueueNames() {
		m.queueMessages.WithLabelValues(queue).Set(float64(counts[queue]))
	}
	oldest := 0.0
	if !oldestArrival.IsZero() {
		oldest = float64(oldestArrival.Unix())
	}
	m.queueOldest.Set(oldest)
	m.queuePollSuccess.Set(float64(time.Now().Unix()))
}

func (m *Metrics) QueuePollFailed() { m.queuePollFailures.Inc() }

func (m *Metrics) QuarantineStored()      { m.quarantine.WithLabelValues("stored").Inc() }
func (m *Metrics) QuarantineReleased()    { m.quarantine.WithLabelValues("released").Inc() }
func (m *Metrics) QuarantineDiscarded()   { m.quarantine.WithLabelValues("discarded").Inc() }
func (m *Metrics) QuarantineLearnedSpam() { m.quarantine.WithLabelValues("learned_spam").Inc() }
