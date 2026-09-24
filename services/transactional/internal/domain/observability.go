package domain

import (
	"errors"
	"fmt"
	"math"
)

// Resultado de un intento de entrega al proveedor, para las metricas. Conjunto cerrado: la
// etiqueta nunca toma un valor que llegue de fuera.
const (
	SendResultSent      = "sent"
	SendResultThrottled = "throttled"
	SendResultPaused    = "paused"
	SendResultTransient = "transient"
	SendResultPermanent = "permanent"
)

// SendResult clasifica el resultado de un intento. Throttled y paused se separan del resto de
// transitorios porque piden acciones distintas: bajar la tasa, o revisar la cuenta en SES.
func SendResult(err error) string {
	if err == nil {
		return SendResultSent
	}
	var se *SendError
	if !errors.As(err, &se) {
		return SendResultTransient
	}
	switch se.Code {
	case "TooManyRequestsException", "LimitExceededException", "Throttling", "ThrottlingException":
		return SendResultThrottled
	case "SendingPausedException", "AccountSuspendedException":
		return SendResultPaused
	}
	if se.Kind == ErrorPermanent {
		return SendResultPermanent
	}
	return SendResultTransient
}

// Motivo por el que se rechaza una notificacion en la ruta de eventos de SES.
const (
	EventRejectTopic          = "topic"
	EventRejectSignature      = "signature"
	EventRejectUnreadable     = "unreadable"
	EventRejectUntagged       = "untagged"
	EventRejectTenantMismatch = "tenant_mismatch"
)

// SESAccountStatus es el estado de la cuenta de SES que se vigila. Las tasas de reputacion son
// nil cuando CloudWatch no tiene dato (cuenta sin envios en la ventana): sin dato no es cero.
type SESAccountStatus struct {
	SendingEnabled   bool
	ProductionAccess bool
	Max24HourSend    float64
	SentLast24Hours  float64
	MaxSendRate      float64
	BounceRate       *float64
	ComplaintRate    *float64
}

// Validate descarta lo que no se puede publicar: un NaN o un infinito que llegara del proveedor
// romperia la serializacion y las comparaciones de las alertas.
func (s SESAccountStatus) Validate() error {
	check := func(name string, v float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("SES: valor no valido en %s: %v", name, v)
		}
		return nil
	}
	for name, v := range map[string]float64{"Max24HourSend": s.Max24HourSend, "SentLast24Hours": s.SentLast24Hours, "MaxSendRate": s.MaxSendRate} {
		if err := check(name, v); err != nil {
			return err
		}
	}
	for name, v := range map[string]*float64{"BounceRate": s.BounceRate, "ComplaintRate": s.ComplaintRate} {
		if v == nil {
			continue
		}
		if err := check(name, *v); err != nil {
			return err
		}
		if *v > 1 {
			return fmt.Errorf("SES: %s fuera de rango: %v", name, *v)
		}
	}
	return nil
}

// Reserva de la cuota diaria de SES para el correo transaccional (SES_MARKETING_QUOTA_RESERVE): la
// fraccion de Max24HourSend que el marketing no puede consumir, para que una campana nunca deje sin
// cuota los codigos de acceso ni las recuperaciones de contrasena de todas las empresas.
const (
	DefaultMarketingQuotaReserve = 0.2
	MinMarketingQuotaReserve     = 0.05
	MaxMarketingQuotaReserve     = 0.9
)

// MarketingQuotaOpen dice si el marketing puede seguir saliendo: lo enviado en 24 h (la lectura de
// SES, que incluye lo de cualquier otro emisor de la cuenta, mas sentSince, lo que este proceso envio
// desde esa lectura) queda por debajo de la cuota menos la reserva. Una cuota sin dato no frena.
func (s SESAccountStatus) MarketingQuotaOpen(sentSince int64, reserve float64) bool {
	if s.Max24HourSend <= 0 {
		return true
	}
	return s.SentLast24Hours+float64(sentSince) < s.Max24HourSend*(1-reserve)
}
