package domain

import (
	"errors"
	"regexp"
	"testing"

	"github.com/google/uuid"
)

func TestParseMTASTSMode(t *testing.T) {
	for raw, want := range map[string]MTASTSMode{"none": MTASTSNone, " Testing ": MTASTSTesting, "ENFORCE": MTASTSEnforce} {
		if got, err := ParseMTASTSMode(raw); err != nil || got != want {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "strict", "enforce; mx: *"} {
		if _, err := ParseMTASTSMode(raw); !errors.Is(err, ErrInvalidMTASTSMode) {
			t.Errorf("%q debe rechazarse: %v", raw, err)
		}
	}
}

func TestLasTransicionesEntranYSalenPorTesting(t *testing.T) {
	permitidas := map[[2]MTASTSMode]bool{
		{MTASTSNone, MTASTSTesting}: true, {MTASTSTesting, MTASTSEnforce}: true, {MTASTSTesting, MTASTSNone}: true,
		{MTASTSEnforce, MTASTSTesting}: true,
	}
	for _, from := range []MTASTSMode{MTASTSNone, MTASTSTesting, MTASTSEnforce} {
		for _, to := range []MTASTSMode{MTASTSNone, MTASTSTesting, MTASTSEnforce} {
			err := MTASTSTransition(from, to)
			if want := from == to || permitidas[[2]MTASTSMode{from, to}]; (err == nil) != want {
				t.Errorf("%s a %s: %v", from, to, err)
			}
		}
	}
}

func TestLaVersionCumpleElFormatoDelRFCYCambiaEnCadaModificacion(t *testing.T) {
	ok := regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
	p := NewMTASTSPolicy(uuid.New(), "acme.test", MTASTSTesting)
	seen := map[string]bool{p.PolicyID: true}
	if !ok.MatchString(p.PolicyID) || p.Mode != MTASTSTesting || p.MaxAge != MTASTSTestingMaxAge {
		t.Fatalf("politica nueva: %+v", p)
	}
	for _, mode := range []MTASTSMode{MTASTSEnforce, MTASTSTesting, MTASTSNone, MTASTSTesting} {
		p.Change(mode)
		if !ok.MatchString(p.PolicyID) || seen[p.PolicyID] {
			t.Fatalf("version %q no valida o repetida", p.PolicyID)
		}
		seen[p.PolicyID] = true
	}
}

func TestMXMatchesPlatform(t *testing.T) {
	casos := []struct {
		nombre    string
		publicado []string
		want      bool
	}{
		{"el de la plataforma", []string{"mx.plataforma.example"}, true},
		{"con punto final y mayusculas", []string{"MX.Plataforma.Example."}, true},
		{"varios iguales", []string{"mx.plataforma.example", "mx.plataforma.example."}, true},
		{"ninguno", nil, false},
		{"uno ajeno", []string{"mail.otro.example"}, false},
		{"uno de la plataforma y uno ajeno", []string{"mx.plataforma.example", "mail.otro.example"}, false},
		{"MX nulo", []string{"."}, false},
		{"subdominio del de la plataforma", []string{"a.mx.plataforma.example"}, false},
	}
	for _, c := range casos {
		if got := MXMatchesPlatform(c.publicado, "mx.plataforma.example"); got != c.want {
			t.Errorf("%s: %v", c.nombre, got)
		}
	}
	if MXMatchesPlatform([]string{"mx.plataforma.example"}, "") {
		t.Error("sin MX de plataforma configurado nada casa")
	}
}

func TestElCuerpoEsElDocumentoDelRFC(t *testing.T) {
	p := MTASTSPolicy{Mode: MTASTSEnforce, MaxAge: MTASTSEnforceMaxAge}
	want := "version: STSv1\r\nmode: enforce\r\nmx: mx.plataforma.example\r\nmax_age: 604800\r\n"
	if got := p.Body("MX.Plataforma.Example."); got != want {
		t.Fatalf("cuerpo: %q", got)
	}
}
