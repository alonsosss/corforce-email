package domain

import (
	"strings"
	"testing"
)

func TestTokenDeSesionConCelda(t *testing.T) {
	secret := strings.Repeat("A", 43)
	token := NewSessionToken("pe-02", secret)
	if token != "pe-02."+secret {
		t.Fatalf("token: %q", token)
	}
	if cell, ok := ParseSessionToken(token); !ok || cell != "pe-02" {
		t.Fatalf("celda: %q %v", cell, ok)
	}
	for _, bad := range []string{
		"", ".", secret, "pe-02.", "." + secret, "pe-02." + secret + "A", "PE-02." + secret,
		"pe.02." + secret, "pe-02." + strings.Repeat("*", 43), "pe-02 ." + secret,
	} {
		if cell, ok := ParseSessionToken(bad); ok {
			t.Errorf("%q aceptado con la celda %q", bad, cell)
		}
	}
}
