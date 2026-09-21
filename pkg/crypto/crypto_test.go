package crypto

import (
	"bytes"
	"errors"
	"testing"
)

const (
	llaveA = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	llaveB = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	llaveC = "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f"
)

func anillo(t *testing.T, activa, viejas string) *KeyRing {
	t.Helper()
	t.Setenv("PRUEBA_KEY", activa)
	t.Setenv("PRUEBA_KEYS_OLD", viejas)
	kr, err := LoadKeyRing("PRUEBA_KEY", "PRUEBA_KEYS_OLD")
	if err != nil {
		t.Fatalf("LoadKeyRing: %v", err)
	}
	return kr
}

// Lo cifrado con la llave anterior se re-cifra bajo la activa y, a partir de ahi, se lee
// SIN la vieja: es lo que permite retirarla.
func TestRotateRecifraBajoLaLlaveActiva(t *testing.T) {
	anterior := anillo(t, llaveA, "")
	dato, err := anterior.Encrypt([]byte("contrasena-del-portal"))
	if err != nil {
		t.Fatal(err)
	}

	rotando := anillo(t, llaveB, llaveA)
	if !rotando.HasOldKeys() {
		t.Fatal("HasOldKeys deberia ser true con una llave vieja configurada")
	}
	nuevo, rotado, err := rotando.Rotate(dato)
	if err != nil || !rotado {
		t.Fatalf("Rotate: rotado=%v err=%v; want true, nil", rotado, err)
	}
	if bytes.Equal(nuevo, dato) {
		t.Fatal("el dato re-cifrado no puede ser identico al viejo")
	}

	// Segunda pasada: ya esta bajo la activa, no se toca.
	otra, rotado, err := rotando.Rotate(nuevo)
	if err != nil || rotado || !bytes.Equal(otra, nuevo) {
		t.Fatalf("segunda pasada: rotado=%v err=%v identico=%v; want false, nil, true", rotado, err, bytes.Equal(otra, nuevo))
	}

	retirada := anillo(t, llaveB, "")
	if retirada.HasOldKeys() {
		t.Fatal("HasOldKeys deberia ser false sin llaves viejas")
	}
	plano, err := retirada.Decrypt(nuevo)
	if err != nil || string(plano) != "contrasena-del-portal" {
		t.Fatalf("tras retirar la vieja: %q err=%v", plano, err)
	}
	if _, err := retirada.Decrypt(dato); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("el dato viejo sin la llave vieja deberia ser ilegible, got %v", err)
	}
}

// Un dato que no abre ninguna llave del anillo es un error explicito, no un "sin cambios":
// el que rota tiene que contarlo antes de retirar nada.
func TestRotateSinLlaveQueAbraEsError(t *testing.T) {
	ajeno := anillo(t, llaveC, "")
	dato, err := ajeno.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	kr := anillo(t, llaveB, llaveA)
	out, rotado, err := kr.Rotate(dato)
	if !errors.Is(err, ErrUndecryptable) || rotado || out != nil {
		t.Fatalf("got out=%v rotado=%v err=%v; want nil, false, ErrUndecryptable", out, rotado, err)
	}
}

// Con datos adicionales el cifrado queda atado a su contexto: copiado a otra fila no abre, ni con la
// llave activa ni con una vieja; y lo cifrado sin ellos no abre con ellos (ni al reves).
func TestCifradoConAADSoloAbreConElMismoContexto(t *testing.T) {
	kr := anillo(t, llaveB, llaveA)
	aad := []byte("empresa-1|trabajo-1")
	dato, err := kr.EncryptWithAAD([]byte("secreto"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if plano, err := kr.DecryptWithAAD(dato, aad); err != nil || string(plano) != "secreto" {
		t.Fatalf("mismo contexto: %q %v", plano, err)
	}
	for name, otro := range map[string][]byte{"otro trabajo": []byte("empresa-1|trabajo-2"), "otra empresa": []byte("empresa-2|trabajo-1"), "sin contexto": nil} {
		if _, err := kr.DecryptWithAAD(dato, otro); !errors.Is(err, ErrUndecryptable) {
			t.Errorf("%s: se abrio con un contexto distinto: %v", name, err)
		}
	}
	if _, err := kr.Decrypt(dato); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("lo cifrado con contexto no debe abrir por el camino sin contexto: %v", err)
	}
	sinContexto, err := kr.Encrypt([]byte("secreto"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.DecryptWithAAD(sinContexto, aad); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("lo cifrado sin contexto no debe abrir con contexto: %v", err)
	}

	anterior := anillo(t, llaveA, "")
	viejo, err := anterior.EncryptWithAAD([]byte("secreto"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if plano, err := kr.DecryptWithAAD(viejo, aad); err != nil || string(plano) != "secreto" {
		t.Fatalf("una llave vieja abre con el mismo contexto: %q %v", plano, err)
	}
}

func TestCadaCifradoUsaUnNonceNuevo(t *testing.T) {
	kr := anillo(t, llaveB, "")
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		dato, err := kr.EncryptWithAAD([]byte("igual"), []byte("mismo"))
		if err != nil {
			t.Fatal(err)
		}
		nonce := string(dato[:12])
		if seen[nonce] {
			t.Fatal("nonce repetido")
		}
		seen[nonce] = true
	}
}
