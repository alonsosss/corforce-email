package deliverability

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// minCapsLetters: un asunto corto en mayusculas ("OK", "IVA") no es gritar.
const minCapsLetters = 6

// subjectSpamWords son expresiones que los filtros de contenido puntuan en el asunto, en
// espanol y en ingles. Se comparan sin tildes, en minusculas y por palabra completa.
var subjectSpamWords = []string{
	"gratis", "100% gratis", "sin costo", "gana dinero", "dinero facil", "dinero rapido", "has ganado",
	"premio", "urgente", "actua ahora", "haz clic aqui", "ultima oportunidad", "sin riesgo",
	"garantizado", "credito aprobado", "oferta unica", "compra ahora",
	"free", "100% free", "act now", "click here", "winner", "you have won", "cash bonus", "urgent",
	"risk-free", "risk free", "guaranteed", "limited time", "make money", "earn money", "no cost",
	"buy now", "casino", "lottery", "viagra",
}

var subjectPunctuation = []string{"!!", "$$"}

func subjectIssues(subject string, add func(code, severity string, count int, format string, args ...any)) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return
	}
	add(CodeSubjectAllCaps, SeverityWarning, boolCount(isAllCaps(subject)),
		"El asunto está escrito en mayúsculas")
	punct := 0
	for _, p := range subjectPunctuation {
		punct += strings.Count(subject, p)
	}
	add(CodeSubjectPunctuation, SeverityWarning, punct,
		"El asunto repite signos como !! o $$")
	n := utf8.RuneCountInString(subject)
	add(CodeSubjectTooLong, SeverityWarning, boolCount(n > MaxSubjectChars),
		"El asunto tiene %d caracteres; más de %d se corta en la mayoría de bandejas", n, MaxSubjectChars)
	words := spamWordsIn(subject)
	add(CodeSubjectSpamWords, SeverityWarning, len(words),
		"El asunto contiene expresiones típicas del spam: %s", strings.Join(words, ", "))
}

func isAllCaps(s string) bool {
	letters := 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			letters++
		}
	}
	return letters >= minCapsLetters
}

func spamWordsIn(subject string) []string {
	folded := " " + foldWords(subject) + " "
	found := make([]string, 0)
	for _, w := range subjectSpamWords {
		if strings.Contains(folded, " "+w+" ") {
			found = append(found, w)
		}
	}
	return found
}

// foldWords pasa a minusculas, quita tildes y deja un espacio entre palabras, de modo que
// "Haz clic AQUI!" se compare como "haz clic aqui".
func foldWords(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	plain, _, err := transform.String(t, strings.ToLower(s))
	if err != nil {
		plain = strings.ToLower(s)
	}
	var b strings.Builder
	space := true
	for _, r := range plain {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '%' || r == '-' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
