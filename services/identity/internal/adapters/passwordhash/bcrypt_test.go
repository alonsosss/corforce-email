package passwordhash

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNewBcryptRechazaUnCosteFueraDeRango(t *testing.T) {
	for _, cost := range []int{bcrypt.MinCost - 1, 0, -1, bcrypt.MaxCost + 1} {
		if _, err := NewBcrypt(cost); err == nil {
			t.Errorf("coste %d aceptado", cost)
		}
	}
}

// El hash sale con el coste configurado, no con el que bcrypt pondria por defecto.
func TestHashConElCosteConfigurado(t *testing.T) {
	const cost = bcrypt.MinCost + 1
	h, err := NewBcrypt(cost)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := h.Hash("Correcta-2026!")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := bcrypt.Cost([]byte(hash)); err != nil || got != cost {
		t.Fatalf("coste del hash = %d (%v), se esperaba %d", got, err, cost)
	}
	if err := h.Compare(hash, "Correcta-2026!"); err != nil {
		t.Fatalf("la contrasena buena no coincide: %v", err)
	}
	if err := h.Compare(hash, "no-es-la-contrasena"); err == nil {
		t.Fatal("una contrasena mala coincide")
	}
}

// Solo un hash valido de otro coste se rehace; uno que no es de bcrypt lo rechaza Compare.
func TestNeedsRehashSoloConOtroCoste(t *testing.T) {
	h, err := NewBcrypt(bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	same, err := h.Hash("Correcta-2026!")
	if err != nil {
		t.Fatal(err)
	}
	other, err := bcrypt.GenerateFromPassword([]byte("Correcta-2026!"), bcrypt.MinCost+1)
	if err != nil {
		t.Fatal(err)
	}
	if h.NeedsRehash(same) {
		t.Error("un hash del coste vigente se rehace")
	}
	if !h.NeedsRehash(string(other)) {
		t.Error("un hash de otro coste no se rehace")
	}
	if h.NeedsRehash("no-es-bcrypt") {
		t.Error("un hash que no es de bcrypt se rehace")
	}
}

// El superadmin que siembra el arranque de la plataforma lleva el mismo coste que las cuentas
// que crea identity: si no, su inicio de sesion fallido tarda distinto y lo delata.
func TestElArranqueDeLaPlataformaUsaElMismoCoste(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "ops", "db", "bootstrap-platform.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`gen_salt\('bf',\s*(\d+)\)`).FindAllSubmatch(script, -1)
	if len(m) == 0 {
		t.Fatal("bootstrap-platform.sh ya no hashea con gen_salt('bf', N): revisa que coste usa")
	}
	for _, sub := range m {
		if cost, _ := strconv.Atoi(string(sub[1])); cost != BcryptCost {
			t.Errorf("bootstrap-platform.sh hashea con coste %d; identity con %d", cost, BcryptCost)
		}
	}
}
