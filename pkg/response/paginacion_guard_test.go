package response_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Guarda de la paginacion. Dos fallos que no se ven leyendo un diff y que costaron
// pantallas mostrando una fraccion de los datos sin error alguno:
//
//   - Un tope que DEGRADA: `if perPage < 1 || perPage > 100 { perPage = 20 }` responde
//     20 filas a quien pidio 500. Debe recortar al maximo, no caer al valor por defecto.
//   - Una meta SIN total_pages: el cliente que recorre paginas (collectAllPages) no
//     puede saber si quedan mas y se detiene en la primera.
//
// Ambos se detectan sobre el codigo porque en ejecucion no fallan: responden 200.

var (
	topeQueDegrada = regexp.MustCompile(`if perPage < 1 \|\| perPage > \d+ \{`)
	metaLiteral    = regexp.MustCompile(`(?s)&response\.Meta\{(.*?)\}`)
)

func raizDelRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no se encontro la raiz del repositorio")
	return ""
}

func recorrerGo(t *testing.T, raiz string, fn func(ruta, src string)) {
	t.Helper()
	for _, base := range []string{"services", "pkg"} {
		err := filepath.Walk(filepath.Join(raiz, base), func(ruta string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(ruta, ".go") || strings.HasSuffix(ruta, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(ruta)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(raiz, ruta)
			fn(rel, string(b))
			return nil
		})
		if err != nil {
			t.Fatalf("recorrer %s: %v", base, err)
		}
	}
}

func TestElTopeDePaginaRecortaNoDegrada(t *testing.T) {
	var malos []string
	recorrerGo(t, raizDelRepo(t), func(ruta, src string) {
		if topeQueDegrada.MatchString(src) {
			malos = append(malos, ruta)
		}
	})
	if len(malos) > 0 {
		t.Errorf("estos listados DEGRADAN al valor por defecto cuando se pide de mas, en vez de recortar al maximo:\n\n"+
			"  %s\n\n"+
			"Quien pide per_page=500 recibe 20 filas y un 200: no hay error que delate el recorte.\n"+
			"Separa las dos decisiones:\n"+
			"    if perPage < 1 { perPage = <defecto> }\n"+
			"    if perPage > <tope> { perPage = <tope> }\n",
			strings.Join(malos, "\n  "))
	}
}

func TestTodaMetaPaginadaLlevaTotalPages(t *testing.T) {
	var malos []string
	recorrerGo(t, raizDelRepo(t), func(ruta, src string) {
		for _, m := range metaLiteral.FindAllStringSubmatch(src, -1) {
			cuerpo := m[1]
			// Solo interesan las metas de un listado paginado (las que llevan pagina).
			if !strings.Contains(cuerpo, "Page:") || strings.Contains(cuerpo, "TotalPages") {
				continue
			}
			malos = append(malos, ruta)
			break
		}
	})
	if len(malos) > 0 {
		t.Errorf("estas metas de listado no llevan total_pages:\n\n  %s\n\n"+
			"Un cliente que recorre paginas lo usa para\n"+
			"saber si quedan mas; sin el se detiene en la primera y la pantalla muestra 100\n"+
			"filas creyendo que son todas. Usa response.PageMeta(total, page, perPage), que lo\n"+
			"calcula, en vez de armar el literal a mano.\n",
			strings.Join(malos, "\n  "))
	}
}
