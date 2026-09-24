package render

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

// Comentarios condicionales de Outlook. html/template descarta todo comentario, y el HTML que
// genera MJML depende de ellos: Outlook de escritorio (motor de Word) solo respeta las columnas y
// los anchos de las tablas de respaldo que van dentro de <!--[if mso]>...<![endif]-->. Se apartan
// antes de compilar, cada uno sustituido por una marca de texto neutra, y se reponen tal cual al
// renderizar. Su contenido no pasa por el escapador, asi que no puede llevar acciones de plantilla;
// las construcciones prohibidas (script, iframe, on*, javascript:) ya se buscan en el HTML entero,
// comentarios incluidos.
var (
	// Apertura y cierre de un bloque visible para todos menos Outlook: <!--[if !mso]><!--> y
	// <!--<![endif]-->. Su contenido es HTML normal y se compila como el resto.
	conditionalReveal = regexp.MustCompile(`(?i)<!--\[if [^\]<>]{1,64}\]><!-->|<!--<!\[endif\]-->`)
	// Bloque que solo ve Outlook: <!--[if mso]>...<![endif]-->, entero.
	conditionalHidden = regexp.MustCompile(`(?is)<!--\[if [^\]<>]{1,64}\]>.*?<!\[endif\]-->`)
)

// conditionalPrefix empieza la marca de cada comentario apartado; le sigue un sufijo aleatorio por
// compilacion (un dato del contacto no puede adivinarlo y hacer aparecer un comentario) y el indice.
// Solo letras y digitos: html/template la deja igual en cualquier contexto de texto o de atributo.
const conditionalPrefix = "CFMCOND"

// maxConditionals acota cuantos comentarios se apartan: MJML genera unos pocos por seccion.
const maxConditionals = 2000

// conditionals son los comentarios apartados de una plantilla y la marca que los sustituye.
type conditionals struct {
	kept []string
	re   *regexp.Regexp
}

// extractConditionals sustituye cada comentario condicional por su marca y los devuelve en orden.
func extractConditionals(src string) (string, *conditionals, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", nil, err
	}
	marker := conditionalPrefix + strings.ToUpper(hex.EncodeToString(nonce[:])) + "N"
	var kept []string
	var failure error
	replace := func(m string) string {
		if failure != nil {
			return m
		}
		if strings.Contains(m, "{{") {
			failure = fmt.Errorf("%w: html: un comentario condicional de Outlook no puede llevar variables", domain.ErrInvalidTemplate)
			return m
		}
		if len(kept) >= maxConditionals {
			failure = fmt.Errorf("%w: html: más de %d comentarios condicionales", domain.ErrInvalidTemplate, maxConditionals)
			return m
		}
		kept = append(kept, m)
		return marker + strconv.Itoa(len(kept)-1) + "X"
	}
	out := conditionalReveal.ReplaceAllStringFunc(src, replace)
	out = conditionalHidden.ReplaceAllStringFunc(out, replace)
	if failure != nil {
		return "", nil, failure
	}
	return out, &conditionals{kept: kept, re: regexp.MustCompile(marker + `(\d+)X`)}, nil
}

// restore repone los comentarios apartados en la salida ya renderizada.
func (c *conditionals) restore(out string) (string, error) {
	if c == nil || len(c.kept) == 0 {
		return out, nil
	}
	restored := c.re.ReplaceAllStringFunc(out, func(m string) string {
		i, err := strconv.Atoi(c.re.FindStringSubmatch(m)[1])
		if err != nil || i < 0 || i >= len(c.kept) {
			return m
		}
		return c.kept[i]
	})
	if len(restored) > domain.MaxOutputBytes {
		return "", domain.ErrOutputTooLarge
	}
	return restored, nil
}
