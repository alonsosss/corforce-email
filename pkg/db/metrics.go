package db

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// El agotamiento del pool es el modo de fallo caracteristico de una plataforma con una
// base por empresa: el servicio sigue vivo y respondiendo el healthcheck mientras todas
// sus peticiones esperan una conexion libre. Estas series lo hacen visible antes de que
// se traduzca en tiempos de espera del usuario.
var (
	poolConnsDesc = prometheus.NewDesc(
		"pgx_pool_connections",
		"Conexiones del pool por estado (acquired, idle, total).",
		[]string{"pool", "state"}, nil,
	)
	poolMaxDesc = prometheus.NewDesc(
		"pgx_pool_max_connections",
		"Tope de conexiones configurado para el pool.",
		[]string{"pool"}, nil,
	)
	poolWaitDesc = prometheus.NewDesc(
		"pgx_pool_acquire_waits_total",
		"Adquisiciones que tuvieron que esperar a que se liberara una conexion.",
		[]string{"pool"}, nil,
	)
)

type poolRegistry struct {
	mu    sync.RWMutex
	pools map[string]*pgxpool.Pool
}

var pools = &poolRegistry{pools: make(map[string]*pgxpool.Pool)}

func init() {
	prometheus.MustRegister(pools)
}

// RegisterPoolMetrics expone un pool en /metrics bajo el nombre dado. Registrar dos veces
// el mismo nombre reemplaza al anterior (un pool recreado sustituye al que cerro).
func RegisterPoolMetrics(name string, p *pgxpool.Pool) {
	if p == nil || name == "" {
		return
	}
	pools.mu.Lock()
	pools.pools[name] = p
	pools.mu.Unlock()
}

// UnregisterPoolMetrics retira un pool cerrado para no publicar cifras congeladas.
func UnregisterPoolMetrics(name string) {
	pools.mu.Lock()
	delete(pools.pools, name)
	pools.mu.Unlock()
}

func (r *poolRegistry) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolConnsDesc
	ch <- poolMaxDesc
	ch <- poolWaitDesc
}

func (r *poolRegistry) Collect(ch chan<- prometheus.Metric) {
	r.mu.RLock()
	snapshot := make(map[string]*pgxpool.Pool, len(r.pools))
	for name, p := range r.pools {
		snapshot[name] = p
	}
	r.mu.RUnlock()

	for name, p := range snapshot {
		s := p.Stat()
		ch <- prometheus.MustNewConstMetric(poolConnsDesc, prometheus.GaugeValue, float64(s.AcquiredConns()), name, "acquired")
		ch <- prometheus.MustNewConstMetric(poolConnsDesc, prometheus.GaugeValue, float64(s.IdleConns()), name, "idle")
		ch <- prometheus.MustNewConstMetric(poolConnsDesc, prometheus.GaugeValue, float64(s.TotalConns()), name, "total")
		ch <- prometheus.MustNewConstMetric(poolMaxDesc, prometheus.GaugeValue, float64(s.MaxConns()), name)
		ch <- prometheus.MustNewConstMetric(poolWaitDesc, prometheus.CounterValue, float64(s.EmptyAcquireCount()), name)
	}
}
