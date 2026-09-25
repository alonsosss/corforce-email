package totp

import (
	"strings"
	"testing"
	"time"
)

// Secreto del apendice B de RFC 6238 ("12345678901234567890" en base32) y sus vectores SHA-1 de 8
// cifras recortados a las 6 ultimas, que es como los calcula este paquete.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestGenerateCoincideConLosVectoresDeRFC6238(t *testing.T) {
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for unix, want := range cases {
		got, err := Generate(rfcSecret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("t=%d: %q %v, se esperaba %q", unix, got, err, want)
		}
	}
}

func TestValidateStepDevuelveElPasoDeLaVentanaQueCoincide(t *testing.T) {
	now := time.Unix(1234567890, 0)
	counter := now.Unix() / period
	for _, delta := range []int64{-1, 0, 1} {
		code, err := Generate(rfcSecret, now.Add(time.Duration(delta*period)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		step, ok := ValidateStep(rfcSecret, code, now)
		if !ok || step != counter+delta {
			t.Errorf("delta %d: paso %d ok=%v, se esperaba %d", delta, step, ok, counter+delta)
		}
	}
}

func TestValidateStepRechazaFueraDeVentanaYEntradasMalFormadas(t *testing.T) {
	now := time.Unix(1234567890, 0)
	lejos, _ := Generate(rfcSecret, now.Add(2*period*time.Second))
	valido, _ := Generate(rfcSecret, now)
	for name, code := range map[string]string{
		"dos ventanas despues": lejos, "vacio": "", "corto": valido[:5], "largo": valido + "0", "letras": "abcdef",
	} {
		if _, ok := ValidateStep(rfcSecret, code, now); ok {
			t.Errorf("%s: se acepto %q", name, code)
		}
	}
	if _, ok := ValidateStep("no-es-base32!", valido, now); ok {
		t.Error("un secreto ilegible no valida nada")
	}
}

func TestValidateSigueAceptandoElCodigoActual(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	code, _ := Generate(secret, time.Now())
	if !Validate(secret, code) {
		t.Fatal("el codigo actual no valida")
	}
	if !Validate(strings.ToLower(secret), code) {
		t.Fatal("el secreto en minusculas no valida")
	}
}
