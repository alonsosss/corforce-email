package domain

import (
	"errors"
	"strings"
)

// ErrImportTooManyCards: el fichero trae mas tarjetas de las que admite una importacion.
var ErrImportTooManyCards = errors.New("el fichero trae más tarjetas de las que admite una importación")

// SplitVCards parte un fichero .vcf con varias tarjetas en el texto de cada una, sin validarlas: cada
// tarjeta se valida despues por separado, de modo que una mala no impide importar las demas. Lo que queda
// fuera de un BEGIN:VCARD ... END:VCARD se ignora; una tarjeta sin cerrar se devuelve tal cual y la
// validacion la rechaza. Mas de maxCards tarjetas es ErrImportTooManyCards.
func SplitVCards(raw string, maxCards int) ([]string, error) {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	var cards []string
	var current []string
	depth := 0
	flush := func() error {
		if len(cards) == maxCards {
			return ErrImportTooManyCards
		}
		cards = append(cards, strings.Join(current, "\r\n")+"\r\n")
		current = nil
		return nil
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.EqualFold(trimmed, "BEGIN:VCARD"):
			depth++
		case depth == 0:
			continue
		}
		current = append(current, line)
		if strings.EqualFold(trimmed, "END:VCARD") {
			if depth--; depth == 0 {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(current) > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return cards, nil
}

// EnsureUID devuelve la tarjeta con UID: si no trae ninguno (las exportaciones de algunos proveedores no lo
// llevan) le anade uid antes del END:VCARD. Una tarjeta mal formada se devuelve sin tocar y la rechaza la
// validacion.
func EnsureUID(card, uid string) string {
	lines := unfold(card)
	if len(lines) < 2 || !strings.EqualFold(strings.TrimSpace(lines[len(lines)-1]), "END:VCARD") {
		return card
	}
	for _, text := range lines {
		if l, ok := parseRawLine(text); ok && l.name == "UID" {
			return card
		}
	}
	trimmed := strings.TrimRight(card, "\r\n")
	end := strings.LastIndex(trimmed, "\n")
	if end < 0 {
		return card
	}
	return trimmed[:end+1] + "UID:" + escapeText(uid) + "\r\n" + trimmed[end+1:] + "\r\n"
}
