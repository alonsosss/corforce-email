package domain

// Motivos de denegacion de la autorizacion previa a un envio.
const (
	ReasonSuspended            = "suspended"
	ReasonReputationRestricted = "reputation_restricted"
	ReasonRateLimited          = "rate_limited"
)

// MaxAuthorizeCount acota una sola autorizacion: un lote mayor se autoriza por tramos.
const MaxAuthorizeCount int64 = 10000

// Usage es el uso de una ventana de tasa. Used es nil cuando no se sabe: el paso no se
// alcanzo o Redis no respondio.
type Usage struct {
	Limit int64
	Used  *int64
}

// Decision es la respuesta a "puede esta empresa enviar N mensajes de esta clase ahora".
type Decision struct {
	Allowed bool
	Class   Class
	State   State
	// Reason explica la denegacion; vacio si se autoriza.
	Reason string
	// RetryAfterSeconds solo acompana a rate_limited cuando la cantidad cabe al abrir la
	// siguiente ventana.
	RetryAfterSeconds *int64
	Hourly            Usage
	Daily             Usage
	// Monthly es nil cuando no se consulto a billing o billing no respondio.
	Monthly *MonthlyUsage
}
