// Package mailreconcile concilia los datos que un servicio de empresa guarda por id de buzon con los buzones
// que existen en mail-directory. Los servicios borran esos datos al recibir mail.mailbox.deleted, pero un
// buzon borrado antes de que existiera el consumidor, o mas alla de lo que el stream conserva, o una
// peticion en vuelo que escribio despues del evento, dejan filas huerfanas que ningun evento va a retirar.
// El barrido las encuentra comparando cada id con el directorio y las borra con las mismas garantias que el
// consumidor: por id de buzon, por empresa y de forma idempotente.
//
// El directorio es la unica fuente de la existencia de un buzon: el barrido nunca borra por una respuesta que
// no obtuvo (error, plazo, cuerpo ilegible), solo por un id que una respuesta correcta no incluye. Ademas
// no toca nada que pueda ser de un buzon recien creado (ventana de gracia) y acota cuanto borra por empresa
// en cada pasada, de modo que un directorio que respondiera mal no vaciaria una empresa de golpe.
package mailreconcile

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

const (
	// startDelay es lo que espera la primera pasada tras arrancar: deja que el servicio termine de levantarse y
	// que un despliegue con varias replicas no las lance todas a la vez.
	startDelay = time.Minute

	// maxBatchesPerTenant es un tope de seguridad del recorrido de una empresa: con lotes de cientos de buzones
	// no se alcanza nunca, y evita un bucle si el almacen devolviera siempre lo mismo.
	maxBatchesPerTenant = 10000
)

var (
	runs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mailbox_reconcile_runs_total",
		Help: "Pasadas del barrido de conciliacion de buzones borrados, por resultado (ok, partial, skipped_not_leader).",
	}, []string{"result"})
	orphans = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "mailbox_reconcile_orphans_purged_total",
		Help: "Buzones borrados cuyos datos huerfanos retiro el barrido de conciliacion.",
	})
	failures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mailbox_reconcile_failures_total",
		Help: "Fallos del barrido de conciliacion por etapa (list, directory, purge, tenants). Ninguno borra nada.",
	}, []string{"stage"})
	capped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "mailbox_reconcile_purge_capped_total",
		Help: "Veces que una empresa alcanzo el tope de buzones retirados en una pasada; el resto espera a la siguiente.",
	})
)

func init() {
	prometheus.MustRegister(runs, orphans, failures, capped)
	for _, r := range []string{"ok", "partial", "skipped_not_leader"} {
		runs.WithLabelValues(r)
	}
	for _, s := range []string{"list", "directory", "purge", "tenants"} {
		failures.WithLabelValues(s)
	}
}

// Directory dice cuales de los buzones pedidos existen en la empresa.
type Directory interface {
	// Existing devuelve los ids de ids que son buzones de la empresa. Un error significa que no se sabe, no
	// que no existan.
	Existing(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]struct{}, error)
}

// Store son los datos por buzon de un servicio, en la base de cada empresa.
type Store interface {
	// StaleMailboxes devuelve hasta limit ids de buzones con datos, en orden ascendente y mayores que after,
	// cuyo dato mas antiguo es anterior a before: el dato de un buzon recien creado no cuenta.
	StaleMailboxes(ctx context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error)
	// PurgeMailbox borra los datos del buzon con las mismas garantias que el consumidor de
	// mail.mailbox.deleted y devuelve cuantos elementos retiro.
	PurgeMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int, error)
}

// Tenants recorre las empresas activas, dando a fn un contexto ya ligado a la base de cada una.
type Tenants interface {
	ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID)) error
}

// LeaderLock toma el cerrojo de "una sola instancia a la vez" y devuelve como soltarlo.
type LeaderLock func(ctx context.Context) (release func(), ok bool)

// Config es la configuracion del barrido. Interval cero lo desactiva.
type Config struct {
	// Interval es cada cuanto corre una pasada.
	Interval time.Duration
	// Grace es la edad minima del dato mas antiguo de un buzon para considerarlo: un buzon recien creado, o una
	// escritura que acaba de llegar, no se concilia hasta pasada la gracia.
	Grace time.Duration
	// BatchSize es cuantos buzones se preguntan al directorio por llamada.
	BatchSize int
	// MaxPurgesPerTenant es cuantos buzones retira como mucho por empresa en una pasada.
	MaxPurgesPerTenant int
}

// Enabled dice si el barrido esta activo.
func (c Config) Enabled() bool { return c.Interval > 0 }

// Sweeper es el barrido de un servicio.
type Sweeper struct {
	name      string
	cfg       Config
	lock      LeaderLock
	tenants   Tenants
	directory Directory
	store     Store
	logger    *zap.Logger
	now       func() time.Time
}

// Deps son las dependencias del barrido. Now es el reloj (nil, time.Now).
type Deps struct {
	Name      string
	Config    Config
	Lock      LeaderLock
	Tenants   Tenants
	Directory Directory
	Store     Store
	Logger    *zap.Logger
	Now       func() time.Time
}

func New(d Deps) *Sweeper {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Sweeper{name: d.Name, cfg: d.Config, lock: d.Lock, tenants: d.Tenants, directory: d.Directory, store: d.Store, logger: logger, now: now}
}

// Report es lo que hizo una pasada.
type Report struct {
	Tenants  int
	Purged   int
	Capped   int
	Failures int
	// Skipped es true si otra replica tenia el cerrojo.
	Skipped bool
}

// Run bloquea hasta que el contexto termina. Sin intervalo no hace nada.
func (s *Sweeper) Run(ctx context.Context) {
	if !s.cfg.Enabled() {
		return
	}
	delay := min(startDelay, s.cfg.Interval)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.Pass(ctx)
		delay = s.cfg.Interval
	}
}

// Pass hace una pasada completa: toma el cerrojo de lider y concilia cada empresa activa. Una empresa que
// falla no detiene a las demas.
func (s *Sweeper) Pass(ctx context.Context) Report {
	release, ok := s.lock(ctx)
	if !ok {
		runs.WithLabelValues("skipped_not_leader").Inc()
		s.logger.Debug(s.name + ": la conciliacion de buzones borrados la ejecuta otra replica")
		return Report{Skipped: true}
	}
	defer release()

	started := s.now()
	var rep Report
	err := s.tenants.ForEach(ctx, func(tctx context.Context, tenantID uuid.UUID) {
		rep.Tenants++
		purged, wasCapped, ok := s.sweepTenant(tctx, tenantID)
		rep.Purged += purged
		if wasCapped {
			rep.Capped++
		}
		if !ok {
			rep.Failures++
		}
	})
	if err != nil {
		if ctx.Err() == nil {
			failures.WithLabelValues("tenants").Inc()
			s.logger.Error(s.name+": conciliacion de buzones borrados: no se pudieron listar las empresas", zap.Error(err))
		}
		rep.Failures++
	}
	result := "ok"
	if rep.Failures > 0 {
		result = "partial"
	}
	runs.WithLabelValues(result).Inc()
	s.logger.Info(s.name+": conciliacion de buzones borrados completada",
		zap.Int("tenants", rep.Tenants), zap.Int("mailboxes_purged", rep.Purged), zap.Int("capped", rep.Capped),
		zap.Int("failures", rep.Failures), zap.Duration("took", s.now().Sub(started)))
	return rep
}

// sweepTenant concilia una empresa. ok es false si algo fallo y quedo por revisar hasta la siguiente pasada;
// en ningun caso un fallo borra nada.
func (s *Sweeper) sweepTenant(ctx context.Context, tenantID uuid.UUID) (purged int, wasCapped, ok bool) {
	before := s.now().Add(-s.cfg.Grace)
	after := uuid.Nil
	for batches := 0; batches < maxBatchesPerTenant; batches++ {
		if ctx.Err() != nil {
			return purged, false, false
		}
		ids, err := s.store.StaleMailboxes(ctx, tenantID, before, after, s.cfg.BatchSize)
		if err != nil {
			failures.WithLabelValues("list").Inc()
			s.logger.Warn(s.name+": conciliacion: no se pudieron leer los buzones con datos; se reintenta en la siguiente pasada",
				zap.String("tenant_id", tenantID.String()), zap.Error(err))
			return purged, false, false
		}
		if len(ids) == 0 {
			return purged, false, true
		}
		existing, err := s.directory.Existing(ctx, tenantID, ids)
		if err != nil {
			failures.WithLabelValues("directory").Inc()
			s.logger.Warn(s.name+": conciliacion: mail-directory no confirmo que buzones existen; no se borra nada y se reintenta en la siguiente pasada",
				zap.String("tenant_id", tenantID.String()), zap.Error(err))
			return purged, false, false
		}
		for _, id := range ids {
			if _, exists := existing[id]; exists {
				continue
			}
			if purged >= s.cfg.MaxPurgesPerTenant {
				capped.Inc()
				s.logger.Warn(s.name+": conciliacion: tope de buzones retirados por pasada alcanzado; el resto queda para la siguiente",
					zap.String("tenant_id", tenantID.String()), zap.Int("max", s.cfg.MaxPurgesPerTenant))
				return purged, true, true
			}
			removed, err := s.store.PurgeMailbox(ctx, tenantID, id)
			if err != nil {
				failures.WithLabelValues("purge").Inc()
				s.logger.Warn(s.name+": conciliacion: no se pudieron retirar los datos de un buzon borrado; se reintenta en la siguiente pasada",
					zap.String("tenant_id", tenantID.String()), zap.String("mailbox_id", id.String()), zap.Error(err))
				return purged, false, false
			}
			purged++
			orphans.Inc()
			s.logger.Info(s.name+": conciliacion: datos de un buzon borrado retirados",
				zap.String("tenant_id", tenantID.String()), zap.String("mailbox_id", id.String()), zap.Int("removed", removed))
		}
		if len(ids) < s.cfg.BatchSize {
			return purged, false, true
		}
		after = ids[len(ids)-1]
	}
	return purged, false, false
}
