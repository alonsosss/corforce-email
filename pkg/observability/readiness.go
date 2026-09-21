package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ReadyPath es la ruta de disponibilidad: a diferencia de /healthz (el proceso responde), dice si las
// dependencias reales del servicio -la base y el bus de eventos- estan al alcance. Un servicio con la
// base caida sigue vivo (reiniciarlo no la arregla, por eso el healthcheck del contenedor usa
// /healthz), pero no puede atender.
const ReadyPath = "/readyz"

// readyCheckTimeout acota cada comprobacion: una dependencia colgada no cuelga la ruta.
const readyCheckTimeout = 2 * time.Second

// ReadinessCheck comprueba una dependencia; nil es disponible.
type ReadinessCheck func(ctx context.Context) error

var (
	readinessMu     sync.RWMutex
	readinessChecks = map[string]ReadinessCheck{}
)

// RegisterReadiness anade (o reemplaza) la comprobacion de una dependencia. Las registran los
// paquetes que abren la dependencia (pkg/db para los pools con nombre, pkg/events para el bus), de
// modo que todo servicio las tiene sin cablearlas uno a uno.
func RegisterReadiness(name string, check ReadinessCheck) {
	readinessMu.Lock()
	readinessChecks[name] = check
	readinessMu.Unlock()
}

func UnregisterReadiness(name string) {
	readinessMu.Lock()
	delete(readinessChecks, name)
	readinessMu.Unlock()
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// readyHandler responde 200 si todas las comprobaciones pasan y 503 si alguna falla. Solo dice
// ok o fail por dependencia: el texto del error de un driver puede llevar host y usuario, y esta
// ruta no lleva ninguna autenticacion (solo se alcanza desde la red interna).
func readyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readinessMu.RLock()
		names := make([]string, 0, len(readinessChecks))
		checks := make(map[string]ReadinessCheck, len(readinessChecks))
		for n, c := range readinessChecks {
			names = append(names, n)
			checks[n] = c
		}
		readinessMu.RUnlock()
		sort.Strings(names)

		results := make(map[string]string, len(names))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, n := range names {
			wg.Add(1)
			go func(name string, check ReadinessCheck) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), readyCheckTimeout)
				defer cancel()
				status := "ok"
				if err := check(ctx); err != nil {
					status = "fail"
				}
				mu.Lock()
				results[name] = status
				mu.Unlock()
			}(n, checks[n])
		}
		wg.Wait()

		out := readyResponse{Status: "ready", Checks: results}
		code := http.StatusOK
		for _, s := range results {
			if s != "ok" {
				out.Status, code = "unready", http.StatusServiceUnavailable
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	})
}
