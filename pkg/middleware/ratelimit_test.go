package middleware

import (
	"testing"
	"time"
)

// Una ventana expresada como numero suelto (NewRateLimiter(100, 60)) son 60 nanosegundos,
// no 60 segundos. Con ese valor el bucle de limpieza duerme 120ns y gira sin parar: un
// servicio estuvo 30 dias consumiendo un nucleo entero por ese caracter. El limitador acota
// la ventana por abajo para que el error no vuelva a costar una CPU.
func TestVentanaAbsurdaNoConvierteLaLimpiezaEnEsperaActiva(t *testing.T) {
	rl := NewRateLimiter(100, 60) // 60 ns: casi con seguridad un error de unidades
	if rl.window < time.Second {
		t.Fatalf("la ventana quedo en %v: el bucle de limpieza giraria en espera activa", rl.window)
	}
}

func TestVentanaRazonableSeRespeta(t *testing.T) {
	rl := NewRateLimiter(100, time.Minute)
	if rl.window != time.Minute {
		t.Fatalf("la ventana legitima no debe alterarse, quedo en %v", rl.window)
	}
}
