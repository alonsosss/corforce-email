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
