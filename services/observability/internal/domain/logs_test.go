package domain

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var ahora = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func consulta(t *testing.T, in LogQueryInput) LogQuery {
	t.Helper()
	q, err := NewLogQuery(in, ahora)
	if err != nil {
		t.Fatalf("%+v: %v", in, err)
	}
	return q
}

func esValidacion(t *testing.T, in LogQueryInput, fragmento string) {
	t.Helper()
	_, err := NewLogQuery(in, ahora)
	var verr *ValidationError
	if !errors.As(err, &verr) || !errors.Is(err, ErrValidation) {
		t.Fatalf("%+v: se esperaba un error de validacion, hubo %v", in, err)
	}
	if !strings.Contains(verr.Msg, fragmento) {
		t.Fatalf("%+v: %q no menciona %q", in, verr.Msg, fragmento)
	}
}

// literalDeGo comprueba que s es exactamente UN literal de cadena entre comillas dobles segun la gramatica
// de Go (la que Loki usa para sus cadenas) y devuelve su valor. Nada puede quedar despues del cierre.
func literalDeGo(t *testing.T, s string) string {
	t.Helper()
	if len(s) < 2 || s[0] != '"' {
		t.Fatalf("no empieza por comilla: %q", s)
	}
	rest := s[1:]
	var out strings.Builder
	for {
		if rest == "" {
			t.Fatalf("literal sin cerrar: %q", s)
		}
		if rest[0] == '"' {
			if rest != `"` {
				t.Fatalf("hay texto despues del cierre del literal: %q", rest[1:])
			}
			return out.String()
		}
		r, _, tail, err := strconv.UnquoteChar(rest, '"')
		if err != nil {
			t.Fatalf("literal invalido %q: %v", s, err)
		}
		out.WriteRune(r)
		rest = tail
	}
}

func TestElTextoEntraComoUnSoloFiltroLiteralAunqueIntenteRomperLaConsulta(t *testing.T) {
	casos := []string{
		`ana@acme.test`,
		`"} |= "` + `x`,
		`"} | json | tenant="otra`,
		`\" or 1=1`,
		`\\`,
		`\`,
		`a"b\c"d`,
		`} != "x" | line_format "{{.tenant}}"`,
		`|~ ".*"`,
		`{servicio="gateway"}`,
		`sender=<ana@acme.test> ` + "é中文",
		"​zero width ",
		`'simple' y "dobles"`,
		"tab nbsp",
	}
	for _, texto := range casos {
		q := consulta(t, LogQueryInput{Service: "postfix-mail", Text: texto})
		logql := q.LogQL()
		prefijo := `{servicio="postfix-mail"} |= `
		if !strings.HasPrefix(logql, prefijo) {
			t.Fatalf("%q: la consulta no empieza por el selector fijo: %s", texto, logql)
		}
		// Lo que sigue al selector y al operador es UN literal que termina con la consulta: cualquier
		// comilla, barra o llave del usuario queda dentro de el.
		literal := logql[len(prefijo):]
		if got := literalDeGo(t, literal); got != strings.TrimSpace(texto) {
			t.Fatalf("%q: el filtro decodifica como %q", texto, got)
		}
		if decodificado, err := strconv.Unquote(literal); err != nil || decodificado != strings.TrimSpace(texto) {
			t.Fatalf("%q: strconv.Unquote(%s) = %q, %v", texto, literal, decodificado, err)
		}
		if !utf8.ValidString(logql) {
			t.Fatalf("%q: LogQL con UTF-8 invalido", texto)
		}
	}
}

func TestSinTextoNoHayFiltroYElServicioVaEntreComillas(t *testing.T) {
	q := consulta(t, LogQueryInput{Service: "gateway", Text: "   "})
	if got := q.LogQL(); got != `{servicio="gateway"}` {
		t.Fatalf("LogQL: %s", got)
	}
}

func TestElTextoRechazaSaltosDeLineaControlUTF8InvalidoYExceso(t *testing.T) {
	esValidacion(t, LogQueryInput{Service: "gateway", Text: "a\nb"}, "saltos de linea")
	esValidacion(t, LogQueryInput{Service: "gateway", Text: "a\rb"}, "saltos de linea")
	esValidacion(t, LogQueryInput{Service: "gateway", Text: "a\tb"}, "control")
	esValidacion(t, LogQueryInput{Service: "gateway", Text: "a\x00b"}, "control")
	esValidacion(t, LogQueryInput{Service: "gateway", Text: "a\x1bb"}, "control")
	esValidacion(t, LogQueryInput{Service: "gateway", Text: "ok\xff"}, "UTF-8")
	esValidacion(t, LogQueryInput{Service: "gateway", Text: strings.Repeat("x", MaxLogTextRunes+1)}, "200")
	if _, err := NewLogQuery(LogQueryInput{Service: "gateway", Text: strings.Repeat("é", MaxLogTextRunes)}, ahora); err != nil {
		t.Fatalf("200 runas multibyte deben valer: %v", err)
	}
}

func TestElServicioSoloPuedeSerDeLaListaBlancaYSeComparaExacto(t *testing.T) {
	for _, malo := range []string{"", "Gateway", "gateway\"} |= \"x", "postgres-primary", "*", `gateway",flujo="stderr`, "mail-*", "gate way"} {
		if _, err := NewLogQuery(LogQueryInput{Service: malo}, ahora); err == nil {
			t.Fatalf("%q paso la lista blanca", malo)
		}
	}
	if _, err := NewLogQuery(LogQueryInput{Service: " gateway "}, ahora); err != nil {
		t.Fatalf("los espacios alrededor se toleran: %v", err)
	}
	if !IsLogService("rspamd-mail") || IsLogService("rspamd") {
		t.Fatal("IsLogService")
	}
}

func TestLaVentanaTieneDefectosYTopes(t *testing.T) {
	q := consulta(t, LogQueryInput{Service: "gateway"})
	if q.Until != ahora || q.Since != ahora.Add(-DefaultLogWindow) || q.Limit != DefaultLogLimit || q.Direction != DirectionBackward {
		t.Fatalf("defectos: %+v", q)
	}
	since := ahora.Add(-3 * time.Hour)
	q = consulta(t, LogQueryInput{Service: "gateway", Since: &since, Limit: 9000, Direction: "forward"})
	if q.Since != since || q.Until != ahora || q.Limit != MaxLogLimit || q.Direction != DirectionForward {
		t.Fatalf("since y forward: %+v", q)
	}
	until := ahora.Add(-time.Hour)
	q = consulta(t, LogQueryInput{Service: "gateway", Until: &until})
	if q.Until != until || q.Since != until.Add(-DefaultLogWindow) {
		t.Fatalf("until sin since: %+v", q)
	}

	enElFuturo := ahora.Add(time.Hour)
	esValidacion(t, LogQueryInput{Service: "gateway", Until: &enElFuturo}, "futuro")
	casiAhora := ahora.Add(time.Minute)
	if _, err := NewLogQuery(LogQueryInput{Service: "gateway", Until: &casiAhora}, ahora); err != nil {
		t.Fatalf("un minuto de adelanto se tolera: %v", err)
	}
	demasiado := ahora.Add(-MaxLogWindow - time.Second)
	esValidacion(t, LogQueryInput{Service: "gateway", Since: &demasiado}, "24 horas")
	justo := ahora.Add(-MaxLogWindow)
	if _, err := NewLogQuery(LogQueryInput{Service: "gateway", Since: &justo}, ahora); err != nil {
		t.Fatalf("24 horas exactas valen: %v", err)
	}
	esValidacion(t, LogQueryInput{Service: "gateway", Since: &ahora, Until: &since}, "anterior")
	esValidacion(t, LogQueryInput{Service: "gateway", Since: &ahora, Until: &ahora}, "anterior")
	esValidacion(t, LogQueryInput{Service: "gateway", Limit: -1}, "limit")
	esValidacion(t, LogQueryInput{Service: "gateway", Direction: "sideways"}, "direction")
}

func TestLasLineasSeOrdenanPorDireccionYSeRecortan(t *testing.T) {
	t0 := ahora
	entradas := []LogEntry{{Timestamp: t0.Add(2 * time.Second), Line: "c"}, {Timestamp: t0, Line: "a"}, {Timestamp: t0.Add(time.Second), Line: "b"}}
	lineas := func(es []LogEntry) string {
		var s []string
		for _, e := range es {
			s = append(s, e.Line)
		}
		return strings.Join(s, "")
	}
	if got := lineas(SortEntries(append([]LogEntry(nil), entradas...), DirectionBackward, 2)); got != "cb" {
		t.Fatalf("backward: %s", got)
	}
	if got := lineas(SortEntries(append([]LogEntry(nil), entradas...), DirectionForward, 0)); got != "abc" {
		t.Fatalf("forward: %s", got)
	}
}

// composeServices lee los nombres de servicio de un fichero de compose (claves de dos espacios bajo
// services:).
func composeServices(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	top := regexp.MustCompile(`^([a-z][a-z0-9-]*):`)
	svc := regexp.MustCompile(`^  ([a-z][a-z0-9-]*):\s*$`)
	out := map[string]bool{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := top.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if section != "services" {
			continue
		}
		if m := svc.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// La lista blanca es exactamente lo que promtail etiqueta: los servicios Go de docker-compose.yml y los
// motores de deploy/mail. Un servicio Go nuevo tiene que entrar aqui, y un nombre que no exista en ningun
// compose es una consulta que nunca devolvera nada.
func TestLaListaBlancaCasaConLosFicherosDeCompose(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	plataforma := composeServices(t, filepath.Join(root, "docker-compose.yml"))
	motores := composeServices(t, filepath.Join(root, "deploy", "mail", "docker-compose.mail.yml"))
	listados := map[string]bool{}
	for _, s := range LogServices() {
		listados[s] = true
		esGo := false
		if _, err := os.Stat(filepath.Join(root, "services", s, "main.go")); err == nil {
			esGo = true
		}
		switch {
		case esGo && !plataforma[s]:
			t.Errorf("%s es un servicio Go que no esta en docker-compose.yml", s)
		case !esGo && !motores[s]:
			t.Errorf("%s no es un servicio Go ni un motor de deploy/mail/docker-compose.mail.yml", s)
		}
	}
	for s := range plataforma {
		if _, err := os.Stat(filepath.Join(root, "services", s, "main.go")); err == nil && !listados[s] {
			t.Errorf("el servicio Go %s falta en la lista blanca del visor", s)
		}
	}
	if len(listados) != len(LogServices()) {
		t.Fatal("la lista blanca tiene nombres repetidos")
	}
}
