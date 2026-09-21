package crypto

import (
	"strings"
	"testing"
)

// El error de una llave mal formada acaba en el registro de arranque del servicio. encoding/hex
// nombra en el el primer caracter no hexadecimal ("invalid byte: U+0067 'g'"), que es un caracter
// de la llave: el mensaje no debe llevar nada de su contenido.
func TestElErrorDeUnaLlaveMalFormadaNoRevelaSuContenido(t *testing.T) {
	malformada := strings.Repeat("a", 63) + "Z"
	t.Setenv("PRUEBA_LLAVE", malformada)
	t.Setenv("PRUEBA_LLAVES_VIEJAS", strings.Repeat("b", 10)+"Q"+strings.Repeat("c", 53))

	_, errRing := LoadKeyRing("PRUEBA_LLAVE", "PRUEBA_LLAVES_VIEJAS")
	_, errMAC := LoadMACKeyRing("PRUEBA_LLAVE", "PRUEBA_LLAVES_VIEJAS")
	t.Setenv("PRUEBA_LLAVE", strings.Repeat("a", 64))
	_, errOldRing := LoadKeyRing("PRUEBA_LLAVE", "PRUEBA_LLAVES_VIEJAS")
	_, errOldMAC := LoadMACKeyRing("PRUEBA_LLAVE", "PRUEBA_LLAVES_VIEJAS")

	for name, err := range map[string]error{"anillo AES": errRing, "anillo MAC": errMAC, "vieja del anillo AES": errOldRing, "vieja del anillo MAC": errOldMAC} {
		if err == nil {
			t.Errorf("%s: una llave mal formada debe rechazarse", name)
			continue
		}
		for _, revela := range []string{"'Z'", "'Q'", "U+005A", "U+0051"} {
			if strings.Contains(err.Error(), revela) {
				t.Errorf("%s: el error revela un caracter de la llave: %v", name, err)
			}
		}
	}
}
