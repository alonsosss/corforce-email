package app

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Config es la configuracion de operador del servicio; main la lee del entorno y la valida.
type Config struct {
	// RunnerConfigured dice si hay clave del ejecutor. Sin ella no se crean trabajos: nadie los
	// ejecutaria y la contrasena de origen quedaria guardada sin motivo.
	RunnerConfigured   bool
	MaxActivePerTenant int
	// MaxJobsPerDay es lo que una empresa puede crear en 24 horas y MaxAuthFailuresPerHour cuantos
	// trabajos contra el mismo servidor y usuario de origen pueden terminar con las credenciales
	// rechazadas en una hora. Acotan el uso de la migracion como sondeo o como fuerza bruta contra
	// cuentas ajenas, porque el ejecutor sale a Internet con la IP de la plataforma. Cero, sin tope.
	MaxJobsPerDay          int
	MaxAuthFailuresPerHour int
	// MaxRunningJobs es cuantos trabajos puede tener reclamados a la vez esta instancia entre todos los
	// ejecutores; ademas cada ejecutor tiene uno a la vez. Cero, sin tope global.
	MaxRunningJobs int
	// Lease es lo que dura una reclamacion sin latido antes de que otro ejecutor pueda tomar el
	// trabajo.
	Lease       time.Duration
	MaxAttempts int
	// SweepInterval es cada cuanto un reclamo sin trabajo conocido recorre todas las empresas.
	SweepInterval time.Duration
	Source        domain.SourcePolicy
}

type Deps struct {
	Repo      ports.JobRepository
	Tx        ports.Transactor
	Mailboxes ports.MailboxDirectory
	Resolver  ports.HostResolver
	Cipher    ports.Cipher
	Tenants   ports.Tenants
	Events    ports.EventPublisher
	Config    Config
	Now       func() time.Time
	Logger    *zap.Logger
}

type UseCase struct {
	repo      ports.JobRepository
	tx        ports.Transactor
	mailboxes ports.MailboxDirectory
	resolver  ports.HostResolver
	cipher    ports.Cipher
	tenants   ports.Tenants
	events    ports.EventPublisher
	cfg       Config
	now       func() time.Time
	logger    *zap.Logger

	// Empresas con trabajo conocido en esta instancia. El ejecutor no conoce empresas: sin esta
	// pista cada reclamo tendria que abrir la base de todas. Lo que llega por otra instancia o por
	// un lease vencido lo encuentra el recorrido completo, como mucho cada SweepInterval.
	mu        sync.Mutex
	hinted    map[uuid.UUID]struct{}
	lastSweep time.Time
	// holds son los trabajos que esta instancia entrego a un ejecutor y siguen vigentes (por su
	// lease, que cada latido renueva), y las reservas de un reclamo en curso. Es memoria de la
	// instancia: con varias, cada una lleva la suya.
	holds map[uuid.UUID]claimHold
}

type claimHold struct {
	runnerID string
	until    time.Time
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{
		repo: d.Repo, tx: d.Tx, mailboxes: d.Mailboxes, resolver: d.Resolver, cipher: d.Cipher,
		tenants: d.Tenants, events: d.Events, cfg: d.Config, now: now, logger: logger,
		hinted: map[uuid.UUID]struct{}{}, holds: map[uuid.UUID]claimHold{},
	}
}

// reserveSlot reserva atomicamente el cupo de un reclamo para runnerID, o dice que no hay: el
// ejecutor ya tiene un trabajo vigente o se alcanzo el tope de trabajos en curso.
func (uc *UseCase) reserveSlot(runnerID string) (uuid.UUID, bool) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	now := uc.now()
	live, mine := 0, 0
	for id, h := range uc.holds {
		if !h.until.After(now) {
			delete(uc.holds, id)
			continue
		}
		live++
		if h.runnerID == runnerID {
			mine++
		}
	}
	if mine > 0 || (uc.cfg.MaxRunningJobs > 0 && live >= uc.cfg.MaxRunningJobs) {
		uc.logger.Warn("mail-migration: reclamo denegado a un ejecutor con trabajo vigente o con el tope de trabajos en curso alcanzado",
			zap.Bool("ejecutor_con_trabajo", mine > 0), zap.Int("en_curso", live))
		return uuid.Nil, false
	}
	slot := uuid.New()
	uc.holds[slot] = claimHold{runnerID: runnerID, until: now.Add(uc.cfg.Lease)}
	return slot, true
}

func (uc *UseCase) bindSlot(slot, jobID uuid.UUID) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	h := uc.holds[slot]
	delete(uc.holds, slot)
	h.until = uc.now().Add(uc.cfg.Lease)
	uc.holds[jobID] = h
}

func (uc *UseCase) extendHold(jobID uuid.UUID, until time.Time) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	if h, ok := uc.holds[jobID]; ok {
		h.until = until
		uc.holds[jobID] = h
	}
}

func (uc *UseCase) releaseSlot(id uuid.UUID) {
	uc.mu.Lock()
	delete(uc.holds, id)
	uc.mu.Unlock()
}

func (uc *UseCase) hint(tenantID uuid.UUID) {
	uc.mu.Lock()
	uc.hinted[tenantID] = struct{}{}
	uc.mu.Unlock()
}

func (uc *UseCase) unhint(tenantID uuid.UUID) {
	uc.mu.Lock()
	delete(uc.hinted, tenantID)
	uc.mu.Unlock()
}

func (uc *UseCase) hints() []uuid.UUID {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	out := make([]uuid.UUID, 0, len(uc.hinted))
	for id := range uc.hinted {
		out = append(out, id)
	}
	return out
}

// sweepDue reserva el siguiente recorrido completo: dos reclamos simultaneos no lo hacen a la vez.
func (uc *UseCase) sweepDue() bool {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	now := uc.now()
	if !uc.lastSweep.IsZero() && now.Sub(uc.lastSweep) < uc.cfg.SweepInterval {
		return false
	}
	uc.lastSweep = now
	return true
}

func NormalizePage(page, perPage int) (int, int, ports.Page) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = DefaultPageSize
	}
	if perPage > MaxPageSize {
		perPage = MaxPageSize
	}
	return page, perPage, ports.Page{Offset: (page - 1) * perPage, Limit: perPage}
}

// inTenant abre el contexto de la base de la empresa y corre fn en una transaccion de ella.
func (uc *UseCase) inTenant(ctx context.Context, tenantID uuid.UUID, fn func(ctx context.Context) error) error {
	tctx, err := uc.tenants.For(ctx, tenantID)
	if err != nil {
		return err
	}
	return uc.tx.Transact(tctx, fn)
}
