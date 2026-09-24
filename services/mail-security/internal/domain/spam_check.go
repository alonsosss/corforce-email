package domain

import (
	"bytes"
	"errors"
	"sort"

	"github.com/shopspring/decimal"
)

// MaxSpamCheckMessageBytes es el mayor MIME que acepta la puntuacion antispam de una plantilla
// (docs/Plan_Editor_Correos.md, seccion 4): un correo de marketing que lo supere ya lo recorta Gmail.
const MaxSpamCheckMessageBytes = 2 << 20

var (
	// ErrSpamCheckEmpty: el cuerpo no trae mensaje que analizar.
	ErrSpamCheckEmpty = errors.New("el mensaje a analizar está vacío")
	// ErrSpamCheckTooLarge: el mensaje supera MaxSpamCheckMessageBytes.
	ErrSpamCheckTooLarge = errors.New("el mensaje a analizar supera 2 MiB")
)

// SpamCheckResult es el veredicto de Rspamd sobre un mensaje que no se entrega: la puntuacion, el umbral de
// rechazo de la celda, la accion que tomaria y los simbolos que la explican, del mas pesado al mas ligero.
type SpamCheckResult struct {
	Score    decimal.Decimal
	Required decimal.Decimal
	Action   string
	Symbols  []SpamCheckSymbol
}

// SpamCheckSymbol es un simbolo con su peso y la descripcion que Rspamd tenga para el. Sus opciones
// (URLs y fragmentos del mensaje) se descartan como en el historial.
type SpamCheckSymbol struct {
	Name        string
	Score       decimal.Decimal
	Description string
}

// SpamCheckOutcome es el desenlace de una puntuacion, la etiqueta de su metrica.
type SpamCheckOutcome string

const (
	SpamCheckScanned       SpamCheckOutcome = "scanned"
	SpamCheckInvalid       SpamCheckOutcome = "invalid"
	SpamCheckUnavailable   SpamCheckOutcome = "unavailable"
	SpamCheckNotConfigured SpamCheckOutcome = "not_configured"
)

func SpamCheckOutcomes() []SpamCheckOutcome {
	return []SpamCheckOutcome{SpamCheckScanned, SpamCheckInvalid, SpamCheckUnavailable, SpamCheckNotConfigured}
}

// ValidateSpamCheckMessage exige un mensaje con contenido y dentro del tope.
func ValidateSpamCheckMessage(msg []byte) error {
	if len(bytes.TrimSpace(msg)) == 0 {
		return ErrSpamCheckEmpty
	}
	if len(msg) > MaxSpamCheckMessageBytes {
		return ErrSpamCheckTooLarge
	}
	return nil
}

// SortSpamCheckSymbols ordena por peso descendente y, a igual peso, por nombre: la salida es estable.
func SortSpamCheckSymbols(symbols []SpamCheckSymbol) {
	sort.SliceStable(symbols, func(i, j int) bool {
		if c := symbols[i].Score.Cmp(symbols[j].Score); c != 0 {
			return c > 0
		}
		return symbols[i].Name < symbols[j].Name
	})
}
