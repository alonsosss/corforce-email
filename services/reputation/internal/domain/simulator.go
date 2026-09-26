package domain

import "strings"

// SESSimulatorDomain es el dominio del simulador de buzones de Amazon SES. Lo fija AWS, no la
// plataforma: lo que se envia ahi no cuenta en la cuota ni en las tasas de rebote y queja de la
// cuenta, asi que tampoco debe contar en la reputacion de la empresa. Las pruebas de rebote y
// queja contra el simulador dejaban la tasa de quejas transaccional de la plataforma en 4 %.
const SESSimulatorDomain = "simulator.amazonses.com"

// IsSimulatorAddress dice si una direccion es del simulador de SES.
func IsSimulatorAddress(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(email[at+1:]), SESSimulatorDomain)
}
