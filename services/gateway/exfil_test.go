package main

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

func trailDePrueba(max int, window time.Duration) *auditTrail {
	return &auditTrail{
		reads:       middleware.NewRateLimiter(max, window),
		exfilMax:    int64(max),
		exfilWindow: window,
	}
}

// La alerta sale en la lectura que alcanza el umbral y una sola vez por ventana y usuario,
// no en cada lectura posterior; otro usuario lleva su propia cuenta.
func TestTrackReadAlertaUnaVezAlAlcanzarElUmbral(t *testing.T) {
	a := trailDePrueba(3, time.Minute)
	ctx := context.Background()

	var alertas []int64
	for i := 0; i < 6; i++ {
		if n, alert := a.trackRead(ctx, "usuario-a"); alert {
			alertas = append(alertas, n)
		}
	}
	if len(alertas) != 1 || alertas[0] != 3 {
		t.Fatalf("alertas de usuario-a: %v; se esperaba una sola, con el total en 3", alertas)
	}
	if _, alert := a.trackRead(ctx, "usuario-b"); alert {
		t.Fatal("la cuenta de otro usuario no se mezcla")
	}
}

func TestTrackReadSinUsuarioNoCuenta(t *testing.T) {
	a := trailDePrueba(1, time.Minute)
	if n, alert := a.trackRead(context.Background(), ""); n != 0 || alert {
		t.Fatalf("una lectura sin usuario no se cuenta: %d, %v", n, alert)
	}
}
