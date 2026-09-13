package postgres

import "testing"

// Lo que escribe el usuario se busca como texto: sus % y _ no pueden ampliar la busqueda.
func TestLikePatternEscapaLosComodines(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"ana":       "%ana%",
		"50%":       `%50\%%`,
		"a_b":       `%a\_b%`,
		`c:\dir`:    `%c:\\dir%`,
		"Ana Pérez": "%Ana Pérez%",
	}
	for in, want := range cases {
		if got := likePattern(in); got != want {
			t.Errorf("likePattern(%q) = %q, quiero %q", in, got, want)
		}
	}
}
