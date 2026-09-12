package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// La politica por defecto quedo endurecida para acotar el radio de una sesion
// robada: refresh de 48h (antes 7 dias) y cierre por inactividad a los 60 min. El
// tope de sesiones concurrentes sigue en 0 (sin limite) para no expulsar
// dispositivos legitimos. Cada empresa puede ajustar las tres.
func TestPoliticaPorDefectoEndurecida(t *testing.T) {
	p := DefaultSessionPolicy(uuid.New())
	if p.RefreshTTL() != 48*time.Hour {
		t.Fatalf("el refresh por defecto son 48h, fue %v", p.RefreshTTL())
	}
	if p.MaxConcurrentSessions != 0 {
		t.Fatal("sin limite de sesiones por defecto")
	}
	ahora := time.Now()
	if p.IdleExceeded(ahora.Add(-30*time.Minute), ahora) {
		t.Fatal("30 min no deben superar el limite de inactividad de 60")
	}
	if !p.IdleExceeded(ahora.Add(-90*time.Minute), ahora) {
		t.Fatal("90 min deben superar el limite de inactividad de 60 por defecto")
	}
}

// Una politica nula (empresa sin fila y fallo de lectura) no puede dejar la sesion sin
// duracion: el cero significaria expirada al instante.
func TestPoliticaNulaCaeEnLosValoresSeguros(t *testing.T) {
	var p *SessionPolicy
	if p.RefreshTTL() != 168*time.Hour {
		t.Fatalf("una politica ausente debe usar el valor historico, fue %v", p.RefreshTTL())
	}
	if p.IdleExceeded(time.Now().Add(-time.Hour), time.Now()) {
		t.Fatal("una politica ausente no puede cerrar sesiones")
	}
}

func TestInactividad(t *testing.T) {
	p := &SessionPolicy{IdleTimeoutMinutes: 30}
	ahora := time.Now()

	if p.IdleExceeded(ahora.Add(-29*time.Minute), ahora) {
		t.Fatal("29 minutos no superan el limite de 30")
	}
	if !p.IdleExceeded(ahora.Add(-31*time.Minute), ahora) {
		t.Fatal("31 minutos si lo superan")
	}
}

func TestValidacionDeRangos(t *testing.T) {
	casos := []struct {
		nombre string
		p      SessionPolicy
		valida bool
	}{
		{"minimos", SessionPolicy{RefreshTTLHours: 1, MaxConcurrentSessions: 0, IdleTimeoutMinutes: 0}, true},
		{"maximos", SessionPolicy{RefreshTTLHours: 8760, MaxConcurrentSessions: 100, IdleTimeoutMinutes: 43200}, true},
		{"ttl cero deja la sesion sin duracion", SessionPolicy{RefreshTTLHours: 0}, false},
		{"ttl negativo", SessionPolicy{RefreshTTLHours: -1}, false},
		{"ttl mayor a un ano", SessionPolicy{RefreshTTLHours: 8761}, false},
		{"sesiones negativas", SessionPolicy{RefreshTTLHours: 24, MaxConcurrentSessions: -1}, false},
		{"inactividad fuera de rango", SessionPolicy{RefreshTTLHours: 24, IdleTimeoutMinutes: 43201}, false},
	}
	for _, c := range casos {
		err := c.p.Validate()
		if c.valida && err != nil {
			t.Errorf("%s: deberia ser valida, dio %v", c.nombre, err)
		}
		if !c.valida && err == nil {
			t.Errorf("%s: deberia rechazarse", c.nombre)
		}
	}
}
