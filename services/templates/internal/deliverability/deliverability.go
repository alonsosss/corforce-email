// Package deliverability verifica un correo ya renderizado contra lo que los proveedores de
// correo castigan (docs/Plan_Editor_Correos.md, secciones 3.4 y 3.5). Es logica pura: recibe el
// asunto y el HTML renderizados, los datos del kit de marca y, si la hubo, la puntuacion
// antispam, y devuelve el informe. No hace red ni lee configuracion.
package deliverability

import (
	"fmt"
	"math"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Codigos de las reglas. Son contrato con la interfaz: no se renombran.
const (
	CodeMissingUnsubscribe     = "missing_unsubscribe"
	CodeMissingPhysicalAddress = "missing_physical_address"
	CodeHTMLTooLarge           = "html_too_large"
	CodeImageOnly              = "image_only"
	CodeLinkShortener          = "link_shortener"
	CodeDeceptiveLink          = "deceptive_link"
	CodeForbiddenContent       = "forbidden_content"
	CodeLowTextRatio           = "low_text_ratio"
	CodeMissingAlt             = "missing_alt"
	CodeMissingPreheader       = "missing_preheader"
	CodeSubjectAllCaps         = "subject_all_caps"
	CodeSubjectPunctuation     = "subject_punctuation"
	CodeSubjectTooLong         = "subject_too_long"
	CodeSubjectSpamWords       = "subject_spam_words"
	CodeInsecureLink           = "insecure_link"
	CodeTooManyLinks           = "too_many_links"
	CodeWidthTooLarge          = "width_too_large"
	CodeExternalStylesheet     = "external_stylesheet"
	CodeSpamScoreHigh          = "spam_score_high"
)

// Umbrales de las reglas.
const (
	// MaxHTMLBytes: Gmail recorta el mensaje a partir de 102 KB y esconde lo que queda debajo,
	// que suele ser el enlace de baja y la direccion.
	MaxHTMLBytes = 102 * 1024
	// MinTextChars: por debajo, un correo con imagenes es "solo imagen" para los filtros.
	MinTextChars = 200
	// MinTextImageRatio y ImageTextEquivalent: la proporcion es texto / (texto + imagenes x
	// ImageTextEquivalent). Sin maquetar no se conoce el area real de cada imagen; se cuenta cada
	// una como un bloque de texto de MinTextChars caracteres, de modo que un correo con una sola
	// imagen necesita unos 300 caracteres de texto para no avisar.
	MinTextImageRatio   = 0.6
	ImageTextEquivalent = MinTextChars
	MaxSubjectChars     = 78
	MaxLinks            = 50
	MaxFixedWidthPx     = 640
	// SpamScoreWarning: a partir de aqui Rspamd suele anadir cabeceras de spam en muchos
	// receptores aunque no rechace.
	SpamScoreWarning = 5.0
	// ActionReject es la accion de Rspamd que rechaza el mensaje.
	ActionReject = "reject"
)

type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Count    int    `json:"count"`
}

type Stats struct {
	HTMLBytes      int     `json:"html_bytes"`
	TextChars      int     `json:"text_chars"`
	Images         int     `json:"images"`
	Links          int     `json:"links"`
	TextImageRatio float64 `json:"text_image_ratio"`
}

type SpamSymbol struct {
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	Description string  `json:"description"`
}

// Spam es la puntuacion de Rspamd. Available falso significa que no se pudo obtener y el resto
// de campos no dice nada.
type Spam struct {
	Available bool         `json:"available"`
	Score     float64      `json:"score"`
	Required  float64      `json:"required"`
	Action    string       `json:"action"`
	Symbols   []SpamSymbol `json:"symbols"`
}

// Unavailable es la puntuacion de una verificacion que no pudo consultar a Rspamd.
func Unavailable() Spam { return Spam{Symbols: []SpamSymbol{}} }

type Report struct {
	Passed bool    `json:"passed"`
	Issues []Issue `json:"issues"`
	Stats  Stats   `json:"stats"`
	Spam   Spam    `json:"spam"`
}

// Errors devuelve solo las incidencias de severidad error.
func (r Report) Errors() []Issue {
	out := make([]Issue, 0)
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Input es el correo a verificar. Marketing activa las reglas propias de los envios comerciales
// (baja y direccion). UnsubscribeURL es el valor con el que se renderizo {{.unsubscribe_url}};
// PhysicalAddress, la direccion del kit de marca.
type Input struct {
	Marketing       bool
	Subject         string
	HTML            string
	UnsubscribeURL  string
	PhysicalAddress string
	Spam            Spam
}

// Analyze aplica todas las reglas y devuelve el informe. Las incidencias salen primero los
// errores y despues los avisos, cada grupo en el orden de la tabla de reglas.
func Analyze(in Input) Report {
	doc := inspect(in.HTML, in.UnsubscribeURL)
	stats := Stats{
		HTMLBytes: len(in.HTML),
		TextChars: doc.textChars,
		Images:    doc.images,
		Links:     doc.links,
	}
	stats.TextImageRatio = textImageRatio(doc.textChars, doc.images)

	var issues []Issue
	add := func(code, severity string, count int, format string, args ...any) {
		if count > 0 {
			issues = append(issues, Issue{Code: code, Severity: severity, Message: fmt.Sprintf(format, args...), Count: count})
		}
	}

	if in.Marketing {
		add(CodeMissingUnsubscribe, SeverityError, boolCount(!doc.hasUnsubscribe),
			"El correo no tiene un enlace de baja: use {{.unsubscribe_url}} en el href de un enlace")
		add(CodeMissingPhysicalAddress, SeverityError, boolCount(!hasAddress(doc.text, in.PhysicalAddress)),
			"%s", missingAddressMessage(in.PhysicalAddress))
	}
	add(CodeHTMLTooLarge, SeverityError, boolCount(stats.HTMLBytes > MaxHTMLBytes),
		"El HTML ocupa %d bytes; Gmail recorta a partir de %d y oculta el final del mensaje", stats.HTMLBytes, MaxHTMLBytes)
	add(CodeImageOnly, SeverityError, boolCount(doc.images > 0 && doc.textChars < MinTextChars),
		"El correo tiene imagenes y solo %d caracteres de texto visible; se necesitan al menos %d", doc.textChars, MinTextChars)
	add(CodeLinkShortener, SeverityError, doc.shorteners,
		"Hay enlaces a acortadores publicos; los filtros los bloquean porque ocultan el destino")
	add(CodeDeceptiveLink, SeverityError, doc.deceptive,
		"El texto de un enlace muestra una direccion de otro dominio que la de su destino")
	add(CodeForbiddenContent, SeverityError, doc.forbidden,
		"El HTML contiene script, iframe, formularios, atributos on* o javascript:")

	add(CodeLowTextRatio, SeverityWarning, boolCount(doc.images > 0 && stats.TextImageRatio < MinTextImageRatio),
		"La proporcion de texto frente a imagenes es %.2f; se recomienda al menos %.1f", stats.TextImageRatio, MinTextImageRatio)
	add(CodeMissingAlt, SeverityWarning, doc.missingAlt,
		"Hay imagenes sin atributo alt; muchos clientes no cargan imagenes por defecto")
	add(CodeMissingPreheader, SeverityWarning, boolCount(!doc.hasPreheader),
		"Falta el texto de previsualizacion oculto al principio del cuerpo")
	subjectIssues(in.Subject, add)
	add(CodeInsecureLink, SeverityWarning, doc.insecure,
		"Hay enlaces http://; use https://")
	add(CodeTooManyLinks, SeverityWarning, boolCount(doc.links > MaxLinks),
		"El correo tiene %d enlaces; mas de %d es un rasgo tipico del spam", doc.links, MaxLinks)
	add(CodeWidthTooLarge, SeverityWarning, doc.wideElements,
		"Hay anchos fijos de mas de %d px; el correo no se adaptara a pantallas pequenas", MaxFixedWidthPx)
	add(CodeExternalStylesheet, SeverityWarning, doc.externalStyles,
		"Hay hojas de estilo externas o @import; la mayoria de clientes de correo las ignoran")
	spamIssue(in.Spam, add)

	ordered := make([]Issue, 0, len(issues))
	for _, sev := range []string{SeverityError, SeverityWarning} {
		for _, i := range issues {
			if i.Severity == sev {
				ordered = append(ordered, i)
			}
		}
	}
	spam := in.Spam
	if spam.Symbols == nil {
		spam.Symbols = []SpamSymbol{}
	}
	report := Report{Issues: ordered, Stats: stats, Spam: spam}
	report.Passed = len(report.Errors()) == 0
	return report
}

func spamIssue(s Spam, add func(code, severity string, count int, format string, args ...any)) {
	if !s.Available {
		return
	}
	switch {
	case s.Action == ActionReject || (s.Required > 0 && s.Score >= s.Required):
		add(CodeSpamScoreHigh, SeverityError, 1,
			"El antispam puntua el correo con %.1f y lo rechazaria (umbral %.1f)", s.Score, s.Required)
	case s.Score >= SpamScoreWarning:
		add(CodeSpamScoreHigh, SeverityWarning, 1,
			"El antispam puntua el correo con %.1f; a partir de %.1f muchos receptores lo marcan", s.Score, SpamScoreWarning)
	}
}

func textImageRatio(textChars, images int) float64 {
	if images == 0 {
		return 1
	}
	r := float64(textChars) / float64(textChars+images*ImageTextEquivalent)
	return math.Round(r*100) / 100
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

func missingAddressMessage(address string) string {
	if address == "" {
		return "Falta la direccion fisica: configurela en el pie legal del kit de marca e incluya el pie en el correo"
	}
	return "El correo no muestra la direccion fisica del kit de marca: incluya el pie legal"
}
