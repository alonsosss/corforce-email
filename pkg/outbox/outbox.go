// Package outbox publica eventos con garantia transaccional: el evento se escribe en la
// misma transaccion que el dato que lo origina y un rele lo entrega despues a JetStream.
// Sin esto, un proceso que cae entre el commit y el Publish pierde el evento y nadie se
// entera (un dominio verificado que nunca llega a Redis, un envio que nadie contabiliza).
//
// La tabla vive en el esquema platform de CADA base (registro, celda y empresa): un
// servicio encola en la base que esta escribiendo y el rele de esa base la vacia. El id
// del evento es la clave de deduplicacion de JetStream, asi que un rele que muere despues
// de publicar y antes de marcar la fila solo produce un duplicado que el servidor descarta.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Execer es lo que necesita Enqueue: el ContextPool de pkg/db, que enruta por la
// transaccion del contexto cuando la hay.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...interface{}) (pgconn.CommandTag, error)
}

// Enqueue guarda el evento para su publicacion. Debe llamarse DENTRO de la transaccion
// de negocio (db.Transact / TransactRLS): asi el evento existe si y solo si el dato existe.
func Enqueue(ctx context.Context, q Execer, subject string, evt events.Event) error {
	if subject == "" {
		return errors.New("outbox: subject vacio")
	}
	if evt.ID == "" {
		evt.ID = uuid.New().String()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("outbox: serializar evento: %w", err)
	}
	var tenantID *string
	if evt.TenantID != "" {
		tenantID = &evt.TenantID
	}
	_, err = q.Exec(ctx,
		`INSERT INTO platform.event_outbox (id, subject, tenant_id, payload) VALUES ($1, $2, $3, $4)`,
		evt.ID, subject, tenantID, payload)
	if err != nil {
		return fmt.Errorf("outbox: encolar %s: %w", subject, err)
	}
	return nil
}

// Publisher es la salida del rele. *events.Bus lo cumple con PublishPersistent.
type Publisher interface {
	PublishPersistent(subject string, evt events.Event) error
}

// Relay vacia la outbox de UNA base.
type Relay struct {
	pool      *pgxpool.Pool
	publisher Publisher
	logger    *zap.Logger
	batch     int
	interval  time.Duration
	// maxAttempts: a partir de aqui la fila se deja marcada como fallida y se avisa; no se
	// reintenta para siempre porque un payload que JetStream rechaza no va a cambiar.
	maxAttempts int
	retention   time.Duration
}

// Options del rele; los ceros toman los valores por defecto.
type Options struct {
	Batch       int
	Interval    time.Duration
	MaxAttempts int
	// Retention > 0 poda lo publicado hace mas de ese tiempo, como mucho una vez por
	// purgeEvery. Con cero la tabla no se poda desde el rele.
	Retention time.Duration
}

// purgeEvery acota la poda: un DELETE por vuelta del rele seria trabajo inutil.
const purgeEvery = time.Hour

// CellRelayLockKey es el cerrojo del rele de la base de una CELDA. Lo comparten todos los
// servicios que encolan en ella (mail-directory, mail-security): con la misma clave, una
// sola instancia de todos ellos vacia la outbox de la celda a la vez y los eventos salen
// en el orden en que se confirmaron.
const CellRelayLockKey int64 = 0x63656c6c6f757462 // "celloutb"

// LeaderLock toma el cerrojo de "una sola instancia a la vez" y devuelve como soltarlo;
// pkg/db.TryLeaderLock lo cumple con el pool y la clave ligados. Se inyecta para no
// importar pkg/db desde aqui.
type LeaderLock func(ctx context.Context) (release func(), ok bool)

func NewRelay(pool *pgxpool.Pool, publisher Publisher, logger *zap.Logger, opt Options) *Relay {
	if opt.Batch <= 0 {
		opt.Batch = 100
	}
	if opt.Interval <= 0 {
		opt.Interval = 2 * time.Second
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 50
	}
	return &Relay{pool: pool, publisher: publisher, logger: logger, batch: opt.Batch, interval: opt.Interval,
		maxAttempts: opt.MaxAttempts, retention: opt.Retention}
}

// Run vacia la outbox hasta que el contexto termina. Varias replicas pueden correrlo a la
// vez: FOR UPDATE SKIP LOCKED reparte las filas sin que dos publiquen la misma.
func (r *Relay) Run(ctx context.Context) {
	r.run(ctx, nil)
}

// RunExclusive es Run, pero en cada vuelta solo vacia quien consigue el cerrojo. Con varias
// replicas (o varios servicios sobre la misma base) Run reparte las filas entre lotes
// concurrentes y un evento posterior puede salir antes que uno anterior; con el cerrojo
// hay un unico rele activo y el orden de confirmacion se conserva salvo en los reintentos.
// Quien no lo consigue lo vuelve a intentar en la siguiente vuelta, asi que la caida del
// rele activo no deja la outbox sin vaciar mas de un intervalo.
func (r *Relay) RunExclusive(ctx context.Context, lock LeaderLock) {
	r.run(ctx, lock)
}

func (r *Relay) run(ctx context.Context, lock LeaderLock) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	var lastPurge time.Time
	for {
		if lock == nil {
			r.turn(ctx, &lastPurge)
		} else if release, ok := lock(ctx); ok {
			r.turn(ctx, &lastPurge)
			release()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// turn es una vuelta del rele: vaciar y, si toca, podar.
func (r *Relay) turn(ctx context.Context, lastPurge *time.Time) {
	r.drainAll(ctx)
	if r.retention <= 0 || time.Since(*lastPurge) < purgeEvery {
		return
	}
	n, err := Purge(ctx, r.pool, r.retention)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("outbox: fallo al podar", zap.Error(err))
		}
		return
	}
	*lastPurge = time.Now()
	if n > 0 {
		r.logger.Info("outbox: eventos publicados podados", zap.Int64("filas", n))
	}
}

// drainAll vacia lotes hasta que uno sale incompleto o falla.
func (r *Relay) drainAll(ctx context.Context) {
	for {
		n, err := r.Drain(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.logger.Warn("outbox: fallo al vaciar", zap.Error(err))
			}
			return
		}
		if n < r.batch {
			return
		}
	}
}

// Drain publica un lote y devuelve cuantas filas proceso (publicadas o marcadas como
// fallidas definitivas).
func (r *Relay) Drain(ctx context.Context) (int, error) {
	return drain(ctx, r.pool, r.publisher, r.logger, r.batch, r.maxAttempts)
}

// pending es una fila de la outbox lista para publicar.
type pending struct {
	id       string
	subject  string
	payload  []byte
	attempts int
}

// drain esta separado del Relay para que el rele por empresa lo reutilice con el pool
// de cada base.
func drain(ctx context.Context, pool *pgxpool.Pool, publisher Publisher, logger *zap.Logger, batch, maxAttempts int) (int, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id, subject, payload, attempts FROM platform.event_outbox
		  WHERE published_at IS NULL AND attempts < $1
		  ORDER BY created_at
		  LIMIT $2 FOR UPDATE SKIP LOCKED`, maxAttempts, batch)
	if err != nil {
		return 0, err
	}
	var items []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.subject, &p.payload, &p.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, p := range items {
		var evt events.Event
		if err := json.Unmarshal(p.payload, &evt); err != nil {
			// Un payload que no se puede leer no se va a poder leer nunca: se agota.
			if _, err := tx.Exec(ctx, `UPDATE platform.event_outbox SET attempts = $2, last_error = $3 WHERE id = $1`, p.id, maxAttempts, "payload ilegible: "+err.Error()); err != nil {
				return 0, err
			}
			logger.Error("outbox: payload ilegible, descartado", zap.String("id", p.id), zap.String("subject", p.subject))
			continue
		}
		evt.ID = p.id
		if err := publisher.PublishPersistent(p.subject, evt); err != nil {
			if _, uerr := tx.Exec(ctx, `UPDATE platform.event_outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, p.id, err.Error()); uerr != nil {
				return 0, uerr
			}
			if p.attempts+1 >= maxAttempts {
				logger.Error("outbox: evento agotado sin publicar", zap.String("id", p.id), zap.String("subject", p.subject), zap.Error(err))
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.event_outbox SET published_at = now(), last_error = NULL WHERE id = $1`, p.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(items), nil
}

// Purge borra lo publicado hace mas de retention. Se llama desde el mismo bucle o desde
// una tarea del scheduler; la tabla no debe crecer sin limite.
func Purge(ctx context.Context, pool *pgxpool.Pool, retention time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM platform.event_outbox WHERE published_at IS NOT NULL AND published_at < now() - $1::interval`, retention.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// TenantLister es lo que necesita el rele por empresa: pkg/db.TenantDB lo cumple.
type TenantLister interface {
	ForEachActiveTenantConcurrent(ctx context.Context, concurrency int, perTenant time.Duration, fn func(ctx context.Context, tenantID string)) error
}

// PoolFromContext obtiene el pool que ForEachActiveTenant dejo en el contexto;
// pkg/db.PoolFromCtx lo cumple. Se inyecta para no importar pkg/db desde aqui.
type PoolFromContext func(ctx context.Context) (*pgxpool.Pool, bool)

// RunForTenants vacia la outbox de cada base de empresa activa, en paralelo con un
// tope, hasta que el contexto termina.
func RunForTenants(ctx context.Context, tenants TenantLister, poolOf PoolFromContext, publisher Publisher, logger *zap.Logger, opt Options) {
	if opt.Batch <= 0 {
		opt.Batch = 100
	}
	if opt.Interval <= 0 {
		opt.Interval = 5 * time.Second
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 50
	}
	t := time.NewTicker(opt.Interval)
	defer t.Stop()
	for {
		err := tenants.ForEachActiveTenantConcurrent(ctx, 4, 30*time.Second, func(tctx context.Context, tenantID string) {
			pool, ok := poolOf(tctx)
			if !ok {
				return
			}
			for {
				n, err := drain(tctx, pool, publisher, logger, opt.Batch, opt.MaxAttempts)
				if err != nil {
					if tctx.Err() == nil {
						logger.Warn("outbox: fallo al vaciar la empresa", zap.String("tenant_id", tenantID), zap.Error(err))
					}
					return
				}
				if n < opt.Batch {
					return
				}
			}
		})
		if err != nil && ctx.Err() == nil {
			logger.Warn("outbox: no se pudo listar las empresas", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
