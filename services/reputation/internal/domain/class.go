// Package domain modela la reputacion de envio de una empresa: las clases de envio, sus
// estados, la regla que decide el estado a partir de las tasas de rebote y queja, y las
// piezas de la autorizacion previa a un envio. No depende de infraestructura.
package domain

import "strings"

// Class es la clase de envio. Cada clase lleva su propia reputacion, sus limites de tasa y
// su derecho mensual: la practica de una no frena a la otra.
type Class string

const (
	ClassTransactional Class = "transactional"
	ClassMarketing     Class = "marketing"
)

// Classes devuelve las clases en un orden estable.
func Classes() []Class {
	return []Class{ClassTransactional, ClassMarketing}
}

// ClassNames devuelve las clases como texto, para validar entradas.
func ClassNames() []string {
	classes := Classes()
	out := make([]string, len(classes))
	for i, c := range classes {
		out[i] = string(c)
	}
	return out
}

// ParseClass valida una clase que llega de fuera.
func ParseClass(s string) (Class, error) {
	switch c := Class(s); c {
	case ClassTransactional, ClassMarketing:
		return c, nil
	}
	return "", ErrInvalidClass
}

// ClassOrDefault aplica el contrato de los eventos de envio: un evento sin clase es de un
// envio transaccional.
func ClassOrDefault(s string) (Class, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ClassTransactional, nil
	}
	return ParseClass(s)
}

// BillingResource es el recurso con el que billing lleva el derecho mensual de la clase.
func (c Class) BillingResource() string {
	return string(c) + "_messages"
}
