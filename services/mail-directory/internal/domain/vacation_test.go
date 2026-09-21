package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func date(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := ParseVacationDate(s)
	if err != nil || d == nil {
		t.Fatalf("fecha de prueba %q: %v", s, err)
	}
	return d
}

func TestVacationSinVentanaGeneraSoloLaAccion(t *testing.T) {
	v := &VacationReply{Enabled: true, Subject: "Fuera de la oficina", Message: "Vuelvo el lunes.", IntervalDays: 3}
	if err := v.Normalize(); err != nil {
		t.Fatal(err)
	}
	want := "require [\"vacation\"];\nvacation :days 3 :subject \"Fuera de la oficina\" \"Vuelvo el lunes.\";\n"
	if !strings.HasSuffix(v.ScriptData, want) {
		t.Fatalf("script:\n%s", v.ScriptData)
	}
	if strings.Contains(v.ScriptData, "currentdate") {
		t.Fatal("sin ventana no debe haber prueba de fechas")
	}
}

func TestVacationConVentanaUsaCurrentdate(t *testing.T) {
	both := &VacationReply{Enabled: true, Message: "Ausente.", StartsOn: date(t, "2026-09-21"), EndsOn: date(t, "2026-09-30")}
	if err := both.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`require ["vacation", "date", "relational"];`,
		`if allof (`,
		`currentdate :value "ge" "date" "2026-09-21"`,
		`currentdate :value "le" "date" "2026-09-30"`,
		`vacation :days 1 "Ausente.";`,
	} {
		if !strings.Contains(both.ScriptData, want) {
			t.Errorf("falta %q en:\n%s", want, both.ScriptData)
		}
	}
	onlyStart := &VacationReply{Enabled: true, Message: "x", StartsOn: date(t, "2026-09-21")}
	if err := onlyStart.Normalize(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(onlyStart.ScriptData, "allof") || !strings.Contains(onlyStart.ScriptData, `if currentdate :value "ge"`) {
		t.Fatalf("con solo inicio es una prueba simple:\n%s", onlyStart.ScriptData)
	}
	onlyEnd := &VacationReply{Enabled: true, Message: "x", EndsOn: date(t, "2026-09-30")}
	if err := onlyEnd.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(onlyEnd.ScriptData, `if currentdate :value "le"`) {
		t.Fatalf("con solo fin es una prueba simple:\n%s", onlyEnd.ScriptData)
	}
}

func TestVacationElTextoNuncaEsCodigoSieve(t *testing.T) {
	v := &VacationReply{Enabled: true, Subject: `a"b\c`, Message: "linea 1\r\nlinea \"2\" con \\ y\ttab\n\ncierre\"; discard; #"}
	if err := v.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.ScriptData, `:subject "a\"b\\c"`) {
		t.Fatalf("asunto sin escapar:\n%s", v.ScriptData)
	}
	if !strings.Contains(v.ScriptData, "\"linea 1\r\nlinea \\\"2\\\" con \\\\ y\ttab\r\n\r\ncierre\\\"; discard; #\";") {
		t.Fatalf("mensaje mal escapado:\n%q", v.ScriptData)
	}
	// La unica accion del script es vacation: lo que el usuario escribio no puede haber creado otra.
	sinComentario := strings.SplitN(v.ScriptData, "\n", 2)[1]
	if strings.Count(sinComentario, "vacation") != 2 || strings.Contains(sinComentario, "\ndiscard") {
		t.Fatalf("aparecio una accion que el usuario no pidio:\n%s", v.ScriptData)
	}
}

func TestVacationUnicodeSeConserva(t *testing.T) {
	v := &VacationReply{Enabled: true, Subject: "Ausencia", Message: "Estaré fuera. 你好 — ñ"}
	if err := v.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.ScriptData, "Estaré fuera. 你好 — ñ") {
		t.Fatalf("se perdio el texto no ASCII:\n%s", v.ScriptData)
	}
}

func TestVacationDesactivadaNoGeneraScript(t *testing.T) {
	v := &VacationReply{Enabled: false, Subject: "s", Message: "guardado para despues", ScriptData: "viejo"}
	if err := v.Normalize(); err != nil {
		t.Fatal(err)
	}
	if v.ScriptData != "" {
		t.Fatalf("una respuesta desactivada no debe servir script: %q", v.ScriptData)
	}
	vacia := &VacationReply{Enabled: false}
	if err := vacia.Normalize(); err != nil || vacia.IntervalDays != DefaultVacationIntervalDays {
		t.Fatalf("desactivada y vacia es valida y toma el intervalo por defecto: %v %d", err, vacia.IntervalDays)
	}
}

func TestVacationValidaciones(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	casos := []struct {
		nombre string
		v      VacationReply
		want   error
	}{
		{"activa sin mensaje", VacationReply{Enabled: true}, ErrVacationMessageRequired},
		{"activa con mensaje en blanco", VacationReply{Enabled: true, Message: "  \n\t "}, ErrVacationMessageRequired},
		{"mensaje con NUL", VacationReply{Enabled: true, Message: "a\x00b"}, ErrVacationMessageInvalid},
		{"mensaje con control", VacationReply{Enabled: true, Message: "a\x07b"}, ErrVacationMessageInvalid},
		{"mensaje con separador de linea Unicode", VacationReply{Enabled: true, Message: "a b"}, ErrVacationMessageInvalid},
		{"mensaje no UTF-8", VacationReply{Enabled: true, Message: "a\xffb"}, ErrVacationMessageInvalid},
		{"mensaje de 8193", VacationReply{Enabled: true, Message: long(MaxVacationMessageRunes + 1)}, ErrVacationMessageInvalid},
		{"asunto con salto de linea", VacationReply{Enabled: true, Message: "x", Subject: "a\nBcc: x@y.z"}, ErrVacationSubjectInvalid},
		{"asunto con retorno", VacationReply{Enabled: true, Message: "x", Subject: "a\rb"}, ErrVacationSubjectInvalid},
		{"asunto con tabulador", VacationReply{Enabled: true, Message: "x", Subject: "a\tb"}, ErrVacationSubjectInvalid},
		{"asunto de 201", VacationReply{Enabled: true, Message: "x", Subject: long(MaxVacationSubjectRunes + 1)}, ErrVacationSubjectInvalid},
		{"intervalo negativo", VacationReply{Enabled: true, Message: "x", IntervalDays: -1}, ErrVacationInterval},
		{"intervalo 31", VacationReply{Enabled: true, Message: "x", IntervalDays: 31}, ErrVacationInterval},
		{"fin anterior al inicio", VacationReply{Enabled: true, Message: "x", StartsOn: date(t, "2026-09-30"), EndsOn: date(t, "2026-09-21")}, ErrVacationWindow},
	}
	for _, c := range casos {
		v := c.v
		if err := v.Normalize(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, se esperaba %v", c.nombre, err, c.want)
		}
	}
	limites := []VacationReply{
		{Enabled: true, Message: long(MaxVacationMessageRunes)},
		{Enabled: true, Message: "x", Subject: long(MaxVacationSubjectRunes)},
		{Enabled: true, Message: "x", IntervalDays: MinVacationIntervalDays},
		{Enabled: true, Message: "x", IntervalDays: MaxVacationIntervalDays},
		{Enabled: true, Message: "x", StartsOn: date(t, "2026-09-21"), EndsOn: date(t, "2026-09-21")},
	}
	for i, v := range limites {
		if err := v.Normalize(); err != nil {
			t.Errorf("limite %d deberia ser valido: %v", i, err)
		}
	}
}

func TestParseVacationDate(t *testing.T) {
	if d, err := ParseVacationDate("  "); d != nil || err != nil {
		t.Fatalf("vacio es sin fecha: %v %v", d, err)
	}
	for _, mala := range []string{"2026-13-01", "21/09/2026", "2026-9-1", "manana", "2026-02-30"} {
		if _, err := ParseVacationDate(mala); !errors.Is(err, ErrVacationDate) {
			t.Errorf("%q deberia ser ErrVacationDate: %v", mala, err)
		}
	}
	d := date(t, "2026-09-21")
	if got := *FormatVacationDate(d); got != "2026-09-21" {
		t.Fatalf("formato %q", got)
	}
	if FormatVacationDate(nil) != nil {
		t.Fatal("nil se queda nil")
	}
}
