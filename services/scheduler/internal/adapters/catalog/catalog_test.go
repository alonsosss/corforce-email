package catalog

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// El catalogo que se despliega (embebido en el binario) tiene que ser valido: si no, el
// servicio no arranca.
func TestElCatalogoDelRepositorioEsValido(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "handlers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("handlers.json: %v", err)
	}
}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`{"handlers":[{"name":"reports.daily","service":"reports","description":"Informe diario","max_timeout_seconds":600,"scopes":["tenant","platform"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	list := c.List()
	if len(list) != 1 || list[0].Service != "reports" || list[0].MaxTimeoutSeconds != 600 || len(list[0].Scopes) != 2 {
		t.Fatalf("catalogo leido: %+v", list)
	}
	for name, raw := range map[string]string{
		"clave desconocida":  `{"handlers":[],"extra":1}`,
		"campo desconocido":  `{"handlers":[{"name":"a.b","service":"r","max_timeout_seconds":1,"scopes":["tenant"],"timeout":2}]}`,
		"sin lista":          `{}`,
		"dos objetos":        `{"handlers":[]}{"handlers":[]}`,
		"no es JSON":         `handlers`,
		"manejador invalido": `{"handlers":[{"name":"a.b","service":"r","max_timeout_seconds":0,"scopes":["tenant"]}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: se esperaba error", name)
		}
	}
}
