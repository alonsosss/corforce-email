package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Ana.Perez+news@Example.COM ": "ana.perez+news@example.com",
		"x@sub.dominio.pe":              "x@sub.dominio.pe",
	}
	for in, want := range cases {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q err=%v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", " ", "sin-arroba", "a@b", "dos@@example.com", "con espacio@example.com", "a@example.c0m", "x@" + strings.Repeat("a", 320) + ".com"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: se esperaba ErrInvalidEmail, hubo %v", bad, err)
		}
	}
}

func TestNormalizeEmailsDescartaInvalidasYRepetidas(t *testing.T) {
	valid, discarded := NormalizeEmails([]string{"A@example.com", "a@example.com", "b@example.com", "roto", ""})
	if len(valid) != 2 || valid[0] != "a@example.com" || valid[1] != "b@example.com" || discarded != 3 {
		t.Fatalf("valid=%v discarded=%d", valid, discarded)
	}
}

func TestReasonSeverityYRemovable(t *testing.T) {
	order := Reasons()
	for i := 1; i < len(order); i++ {
		if order[i-1].Severity() <= order[i].Severity() {
			t.Errorf("%s debe pesar mas que %s", order[i-1], order[i])
		}
	}
	if Reason("soft").Severity() != 0 {
		t.Error("una causa desconocida no pesa")
	}
	if ReasonUnsubscribe.Removable() || !ReasonComplaint.Removable() || !ReasonManual.Removable() {
		t.Error("solo la baja es irretirable por API")
	}
	if _, err := ParseReason("hard_bounce"); err != nil {
		t.Error(err)
	}
	if _, err := ParseReason("soft_bounce"); !errors.Is(err, ErrInvalidReason) {
		t.Error("soft_bounce no es una causa")
	}
}
