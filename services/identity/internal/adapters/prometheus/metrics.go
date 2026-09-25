// Package prometheus expone las metricas propias de identity en el registro que sirve
// pkg/server en /metrics.
package prometheus

import "github.com/prometheus/client_golang/prometheus"

var (
	mfaSecretsReencrypted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "identity_mfa_secrets_reencrypted_total",
		Help: "Secretos del segundo factor re-cifrados bajo la llave activa de MAIL_ENCRYPTION_KEY.",
	})
	mfaSecretsPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "identity_mfa_secrets_pending_reencryption",
		Help: "Secretos del segundo factor que ninguna llave del anillo abrio en la ultima pasada de rotacion. Con uno solo, retirar una llave deja cuentas sin poder entrar.",
	})
)

func init() { prometheus.MustRegister(mfaSecretsReencrypted, mfaSecretsPending) }

// Metrics publica las pasadas de rotacion del secreto del segundo factor.
type Metrics struct{}

func New() Metrics { return Metrics{} }

// MFASecretsReencrypted suma los secretos re-cifrados, tambien los de una pasada interrumpida.
func (Metrics) MFASecretsReencrypted(n int) { mfaSecretsReencrypted.Add(float64(n)) }

// MFASecretsPending publica cuantos no abrio ninguna llave en la ultima pasada completa.
func (Metrics) MFASecretsPending(n int) { mfaSecretsPending.Set(float64(n)) }
