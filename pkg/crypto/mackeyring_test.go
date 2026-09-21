package crypto

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func anilloMAC(t *testing.T, activa, viejas string) *MACKeyRing {
	t.Helper()
	t.Setenv("PRUEBA_MAC_KEY", activa)
	t.Setenv("PRUEBA_MAC_KEYS_OLD", viejas)
	kr, err := LoadMACKeyRing("PRUEBA_MAC_KEY", "PRUEBA_MAC_KEYS_OLD")
	if err != nil {
		t.Fatalf("LoadMACKeyRing: %v", err)
	}
	return kr
}

func TestMACSinLlaveNoHayAnillo(t *testing.T) {
	kr := anilloMAC(t, "", "")
	if kr != nil {
		t.Fatal("sin llave el anillo debe ser nil: el llamador decide si la exige")
	}
}

func TestMACFirmaConHMACSHA256DeLaLlaveActiva(t *testing.T) {
	kr := anilloMAC(t, llaveA, "")
	mac, err := kr.SignWith(kr.ActiveID(), []byte("dato"))
	if err != nil {
		t.Fatal(err)
	}
	llave, _ := hex.DecodeString(llaveA)
	h := hmac.New(sha256.New, llave)
	h.Write([]byte("dato"))
	if !bytes.Equal(mac, h.Sum(nil)) {
		t.Fatal("la firma no es HMAC-SHA256 de la llave activa")
	}
	if len(kr.ActiveID()) != 16 {
		t.Fatalf("id %q", kr.ActiveID())
	}
}

func TestMACElIdentificadorNoRevelaLaLlaveYDependeDeEsta(t *testing.T) {
	a := anilloMAC(t, llaveA, "")
	b := anilloMAC(t, llaveB, "")
	if a.ActiveID() == b.ActiveID() {
		t.Fatal("dos llaves distintas dan el mismo id")
	}
	if strings.Contains(llaveA, a.ActiveID()) {
		t.Fatal("el id es un trozo de la llave")
	}
	if a.ActiveID() != anilloMAC(t, llaveA, "").ActiveID() {
		t.Fatal("el id no es estable para la misma llave")
	}
}

// Tras rotar, lo firmado con la llave anterior se sigue verificando con ella por su id, y
// el id de una llave que ya no esta es un error explicito, no una firma que no cuadra.
func TestMACRotacionVerificaConLaLlaveAnteriorPorSuID(t *testing.T) {
	anterior := anilloMAC(t, llaveA, "")
	idViejo := anterior.ActiveID()
	macViejo, err := anterior.SignWith(idViejo, []byte("fila"))
	if err != nil {
		t.Fatal(err)
	}

	rotando := anilloMAC(t, llaveB, llaveA)
	if !rotando.HasOldKeys() || rotando.ActiveID() == idViejo {
		t.Fatal("la llave nueva debe ser otra y la vieja seguir en el anillo")
	}
	got, err := rotando.SignWith(idViejo, []byte("fila"))
	if err != nil || !bytes.Equal(got, macViejo) {
		t.Fatalf("no reproduce la firma con la llave anterior: %v", err)
	}
	macNuevo, err := rotando.SignWith(rotando.ActiveID(), []byte("fila"))
	if err != nil || bytes.Equal(macNuevo, macViejo) {
		t.Fatal("la llave nueva debe dar otra firma para el mismo dato")
	}

	retirada := anilloMAC(t, llaveB, "")
	if _, err := retirada.SignWith(idViejo, []byte("fila")); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("una llave retirada debe ser ErrUnknownKeyID, got %v", err)
	}
}

func TestMACRechazaConfiguracionIncoherente(t *testing.T) {
	casos := map[string][2]string{
		"retiradas sin activa": {"", llaveA},
		"activa corta":         {"abcd", ""},
		"activa no hex":        {strings.Repeat("z", 64), ""},
		"retirada mal hecha":   {llaveA, "corta"},
		"retirada repetida":    {llaveA, llaveB + "," + llaveB},
		"activa entre viejas":  {llaveA, llaveA},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			t.Setenv("PRUEBA_MAC_KEY", c[0])
			t.Setenv("PRUEBA_MAC_KEYS_OLD", c[1])
			kr, err := LoadMACKeyRing("PRUEBA_MAC_KEY", "PRUEBA_MAC_KEYS_OLD")
			if err == nil || kr != nil {
				t.Fatalf("se esperaba un error, got kr=%v err=%v", kr, err)
			}
			for _, secreto := range []string{llaveA, llaveB} {
				if strings.Contains(err.Error(), secreto) {
					t.Fatal("el error deja ver una llave")
				}
			}
		})
	}
}
