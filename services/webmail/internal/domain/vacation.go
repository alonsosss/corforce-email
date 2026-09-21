package domain

import "time"

// VacationLimits son los topes de la respuesta automatica que aplica mail-directory: la interfaz los
// recibe de ahi y no los copia.
type VacationLimits struct {
	SubjectMaxLength int
	MessageMaxLength int
	IntervalMinDays  int
	IntervalMaxDays  int
}

// Vacation es la respuesta automatica del buzon con el que se abrio la sesion. Las fechas son de
// calendario (AAAA-MM-DD) y se comparan con la del servidor.
type Vacation struct {
	Enabled      bool
	Subject      string
	Message      string
	IntervalDays int
	StartsOn     *string
	EndsOn       *string
	UpdatedAt    *time.Time
	Limits       VacationLimits
}

// VacationInput es lo que el usuario cambia. Reemplaza la respuesta entera.
type VacationInput struct {
	Enabled      bool
	Subject      string
	Message      string
	IntervalDays int
	StartsOn     *string
	EndsOn       *string
}
