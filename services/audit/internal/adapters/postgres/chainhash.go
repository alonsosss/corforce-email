package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Versiones del hash de una fila (docs/adr/0006). La 1 es SHA-256 sin clave sobre campos
// unidos con "|": las filas escritas antes de la version 2 la conservan y se verifican con
// su formula original. La 2 es HMAC-SHA256 con clave sobre una serializacion con prefijo de
// longitud.
const (
	hashVersionUnkeyed = 1
	hashVersionKeyed   = 2
)

// Los cerrojos serializan la escritura de cada cadena dentro de la base de una empresa (cada
// empresa tiene su propia base, asi que el cerrojo es por empresa). Son de transaccion: se
// sueltan solos al confirmar o revertir.
const (
	auditChainLockKey    = 4771001
	securityChainLockKey = 4771002
)

// chainHash calcula el eslabon de version 1 de una fila: SHA-256 del hash anterior mas el
// contenido del evento TAL COMO LO ALMACENA la base. Es clave usar la forma almacenada:
// Postgres normaliza el jsonb (reordena/limpia) y guarda created_at con precision de
// microsegundos, asi que hashear el valor en memoria no cuadraria con lo que se lee al
// verificar. Por eso before/after/changes llegan como el texto del jsonb devuelto por la BD,
// y createdAt es el valor almacenado.
//
// Su formato es un contrato con las filas ya escritas y no se toca: une los campos con "|" sin
// longitud (dos campos contiguos que contengan "|" pueden intercambiar contenido) y deja fuera
// user_agent. Esas dos debilidades son la razon de la version 2.
func chainHash(prev string, l *domain.AuditLog, before, after, changes string, createdAt time.Time) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte('|') }
	w(prev)
	w(l.ID.String())
	w(l.TenantID.String())
	w(l.UserID.String())
	if l.SessionID != nil {
		w(l.SessionID.String())
	} else {
		w("")
	}
	w(l.Action)
	w(l.Module)
	w(l.Resource)
	w(derefStr(l.ResourceID))
	w(l.IPAddress)
	w(derefStr(l.RequestID))
	w(before)
	w(after)
	w(changes)
	w(l.Severity)
	b.WriteString(createdAt.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// canonical serializa los campos de una fila sin ambiguedad: cada campo lleva su longitud de
// 8 bytes, lo opcional lleva un byte de presencia (NULL y cadena vacia son cosas distintas) y
// las fechas van como microsegundos desde la epoca. Dos filas distintas nunca dan los mismos
// bytes, aunque un campo contenga cualquier separador.
type canonical struct{ b []byte }

func (c *canonical) raw(v []byte) {
	c.b = binary.BigEndian.AppendUint64(c.b, uint64(len(v)))
	c.b = append(c.b, v...)
}

func (c *canonical) str(s string) { c.raw([]byte(s)) }

func (c *canonical) optStr(s *string) {
	if s == nil {
		c.b = append(c.b, 0)
		return
	}
	c.b = append(c.b, 1)
	c.str(*s)
}

func (c *canonical) id(u uuid.UUID) { c.raw(u[:]) }

func (c *canonical) optID(u *uuid.UUID) {
	if u == nil {
		c.b = append(c.b, 0)
		return
	}
	c.b = append(c.b, 1)
	c.id(*u)
}

func (c *canonical) int64(v int64) { c.b = binary.BigEndian.AppendUint64(c.b, uint64(v)) }

func (c *canonical) at(t time.Time) { c.int64(t.UTC().UnixMicro()) }

// header abre toda serializacion con la cadena a la que pertenece, la version, la llave y la
// posicion: una fila de una cadena no se puede pasar por una de otra ni moverse de sitio sin la
// llave.
func (c *canonical) header(chain domain.ChainName, keyID string, seq int64, prev string) {
	c.str("cfm-audit-chain")
	c.str(string(chain))
	c.int64(hashVersionKeyed)
	c.str(keyID)
	c.int64(seq)
	c.str(prev)
}

// auditLogCanonicalV2 recibe la fila en su forma ALMACENADA (jsonb como texto de la base,
// created_at con la precision real), por la misma razon que chainHash.
func auditLogCanonicalV2(keyID string, seq int64, prev string, l *domain.AuditLog) []byte {
	var c canonical
	c.header(domain.ChainAuditLogs, keyID, seq, prev)
	c.id(l.ID)
	c.id(l.TenantID)
	c.id(l.UserID)
	c.optID(l.SessionID)
	c.str(l.Action)
	c.str(l.Module)
	c.str(l.Resource)
	c.optStr(l.ResourceID)
	c.str(l.IPAddress)
	c.optStr(l.UserAgent)
	c.optStr(l.RequestID)
	c.optStr(l.Before)
	c.optStr(l.After)
	c.optStr(l.Changes)
	c.str(l.Severity)
	c.at(l.CreatedAt)
	return c.b
}

// securityEventRecord es una fila de security_events tal como la guarda la base. El
// reconocimiento no esta: cambia a proposito y no forma parte de lo que la cadena protege.
type securityEventRecord struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    *uuid.UUID
	EventType string
	IP        *string
	UserAgent *string
	Detail    *string
	RiskLevel string
	CreatedAt time.Time
}

func securityEventCanonicalV2(keyID string, seq int64, prev string, e *securityEventRecord) []byte {
	var c canonical
	c.header(domain.ChainSecurityEvents, keyID, seq, prev)
	c.id(e.ID)
	c.id(e.TenantID)
	c.optID(e.UserID)
	c.str(e.EventType)
	c.optStr(e.IP)
	c.optStr(e.UserAgent)
	c.optStr(e.Detail)
	c.str(e.RiskLevel)
	c.at(e.CreatedAt)
	return c.b
}

// signCanonical firma con la llave activa y devuelve el hash hex. Solo se llama con anillo.
func signCanonical(ring *crypto.MACKeyRing, keyID string, canon []byte) (string, error) {
	mac, err := ring.SignWith(keyID, canon)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac), nil
}

func hashesEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// beginChained abre la transaccion que escribe en una cadena y serializa a sus escritores: dos
// inserciones concurrentes no pueden leer el mismo "ultimo hash" y bifurcar la cadena.
func beginChained(ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, lockKey int64) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// storedRow es lo que el verificador lee de una fila de cualquier cadena. seq es nil si la
// columna esta vacia: una fila con hash y sin posicion no se puede ubicar.
type storedRow struct {
	id       uuid.UUID
	tenantID uuid.UUID
	seq      *int64
	version  int
	keyID    string
	prev     string
	entry    string
}

// chainVerifier lleva el estado de un recorrido: el hash esperado del eslabon anterior, si ya
// se vio una fila de version 2 y el resultado. Recorre las filas por posicion y se detiene en la
// primera que falla.
type chainVerifier struct {
	ring     *crypto.MACKeyRing
	tenantID uuid.UUID
	res      *domain.ChainIntegrity
	prev     string
	sawKeyed bool
	lastSeq  int64
	// lastVersion es la version de hash de la ultima fila con posicion verificada.
	lastVersion int
	// adoptPrev hace que la primera fila lea su enlace de la propia fila: es la comprobacion de la
	// fila de un punto de reanudacion, cuyo eslabon anterior no se conoce (se verifico en la pasada
	// anterior) y cuyo contenido y hash si.
	adoptPrev bool
}

func newChainVerifier(chain domain.ChainName, ring *crypto.MACKeyRing, tenantID uuid.UUID) *chainVerifier {
	return &chainVerifier{
		ring:     ring,
		tenantID: tenantID,
		res:      &domain.ChainIntegrity{OK: true, Chain: chain, Versions: map[string]int{}},
	}
}

// check verifica una fila. unkeyed calcula el hash de version 1 y serialize la serializacion de
// la version 2 para la llave dada; ninguna de las dos se llama si la version no la pide. Un
// unkeyed nil dice que la cadena no tiene version 1. Devuelve false, con el fallo ya anotado, si
// la fila no cuadra.
func (v *chainVerifier) check(row storedRow, unkeyed func() string, serialize func(keyID string, seq int64) []byte) bool {
	v.res.Checked++
	v.res.Versions[strconv.Itoa(row.version)]++
	if row.seq != nil {
		v.lastSeq = *row.seq
		v.lastVersion = row.version
	}
	if v.adoptPrev {
		v.prev, v.adoptPrev = row.prev, false
	}
	switch {
	case row.version == hashVersionUnkeyed && unkeyed != nil:
		if v.sawKeyed {
			return v.fail(row, domain.ReasonHashVersionRegression)
		}
		if row.prev != v.prev || !hashesEqual(unkeyed(), row.entry) {
			return v.fail(row, domain.ReasonChainBroken)
		}
	case row.version == hashVersionKeyed:
		v.sawKeyed = true
		if v.ring == nil {
			return v.fail(row, domain.ReasonHashKeyMissing)
		}
		if row.seq == nil {
			return v.fail(row, domain.ReasonChainBroken)
		}
		got, err := signCanonical(v.ring, row.keyID, serialize(row.keyID, *row.seq))
		if errors.Is(err, crypto.ErrUnknownKeyID) {
			return v.fail(row, domain.ReasonHashKeyUnknown)
		}
		if err != nil || row.prev != v.prev || !hashesEqual(got, row.entry) {
			return v.fail(row, domain.ReasonChainBroken)
		}
	default:
		return v.fail(row, domain.ReasonHashVersionUnsupported)
	}
	v.prev = row.entry
	v.res.Head = &domain.ChainHead{Hash: row.entry, HashVersion: row.version}
	if row.seq != nil {
		v.res.Head.Seq = *row.seq
	}
	return true
}

// fail anota la rotura. La fila de otra empresa que hubiera en la misma base rompe la cadena
// igual, pero no se identifica: este verificador solo nombra filas de la empresa que pregunta.
func (v *chainVerifier) fail(row storedRow, reason string) bool {
	v.res.OK, v.res.Reason, v.res.Head = false, reason, nil
	version := row.version
	v.res.BrokenVersion = &version
	if row.tenantID == v.tenantID {
		id := row.id
		v.res.BrokenID = &id
		v.res.BrokenSeq = row.seq
	}
	return false
}

// resumeFrom coloca el verificador en el punto de una verificacion anterior: lo ya contado, el
// hash del que la siguiente fila debe colgar y si ya se vio version 2 (la version no retrocede).
func (v *chainVerifier) resumeFrom(cp *domain.ChainCheckpoint) {
	v.prev, v.sawKeyed, v.lastSeq, v.lastVersion = cp.Hash, cp.SawKeyed, cp.Seq, cp.HashVersion
	v.res.Checked = cp.Checked
	v.res.Head = &domain.ChainHead{Seq: cp.Seq, Hash: cp.Hash, HashVersion: cp.HashVersion}
	for version, n := range cp.Versions {
		v.res.Versions[version] = n
	}
}

// checkpoint es el punto alcanzado: la fila de posicion lastSeq y su hash.
func (v *chainVerifier) checkpoint() domain.ChainCheckpoint {
	cp := domain.ChainCheckpoint{
		Seq: v.lastSeq, Hash: v.prev, HashVersion: v.lastVersion, SawKeyed: v.sawKeyed, Checked: v.res.Checked,
		Versions: make(map[string]int, len(v.res.Versions)),
	}
	for version, n := range v.res.Versions {
		cp.Versions[version] = n
	}
	return cp
}

// failCheckpoint anota que la fila del punto de reanudacion ya no es la que se verifico.
func (v *chainVerifier) failCheckpoint(cp *domain.ChainCheckpoint) {
	seq := cp.Seq
	v.res.OK, v.res.Reason, v.res.Head, v.res.BrokenSeq = false, domain.ReasonCheckpointMismatch, nil, &seq
}

// chainQueries son las tres consultas de una cadena: por paginacion de clave (seq > $1, LIMIT $2),
// una fila por posicion (seq = $1) y las filas con hash y sin posicion (LIMIT $1).
type chainQueries struct{ keyset, at, orphans string }

// rowScanner lee una fila y la verifica con el verificador que se le da: el de la cadena o el que
// comprueba la fila de un punto de reanudacion.
type rowScanner func(v *chainVerifier, rows pgx.Rows) (bool, error)

// verifyChain recorre una cadena en lotes de verifyBatchSize filas por paginacion de clave. Con
// opts.From no relee lo anterior al punto, pero SI su fila: comprueba que su contenido sigue dando
// su hash y que este es el que se registro. Sin eso, quien editara una fila ya verificada (o
// cambiara el punto guardado por el de otra) quedaria fuera de toda reanudacion. Entre lotes se
// devuelve la conexion; un contexto cancelado o vencido corta el recorrido en el siguiente lote como
// mucho, y opts.OnBatch recibe el punto alcanzado tras cada uno.
func verifyChain(ctx context.Context, pool *db.ContextPool, v *chainVerifier, q chainQueries, scan rowScanner, opts domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	drain := func(rows pgx.Rows, with *chainVerifier) (n int, stop bool, err error) {
		defer rows.Close()
		for rows.Next() {
			ok, err := scan(with, rows)
			if err != nil {
				return n, false, err
			}
			n++
			if !ok {
				return n, true, nil
			}
		}
		return n, false, rows.Err()
	}

	after := int64(math.MinInt64)
	if opts.From != nil {
		if err := checkResumePoint(ctx, pool, v, q.at, scan, opts.From, drain); err != nil || !v.res.OK {
			return v.res, err
		}
		v.resumeFrom(opts.From)
		after = opts.From.Seq
	}
	for {
		rows, err := pool.Query(ctx, q.keyset, after, verifyBatchSize)
		if err != nil {
			return nil, err
		}
		n, stop, err := drain(rows, v)
		if err != nil {
			return nil, err
		}
		if stop {
			return v.res, nil
		}
		if n < verifyBatchSize {
			break
		}
		after = v.lastSeq
		if opts.OnBatch != nil {
			if err := opts.OnBatch(v.checkpoint()); err != nil {
				return nil, err
			}
		}
	}
	// El punto final se toma antes de las filas sin posicion: estas no son parte de la secuencia
	// desde la que continuara la siguiente verificacion.
	final := v.checkpoint()
	final.Complete = true

	rows, err := pool.Query(ctx, q.orphans, verifyBatchSize)
	if err != nil {
		return nil, err
	}
	if _, _, err := drain(rows, v); err != nil {
		return nil, err
	}
	if v.res.OK && v.lastSeq > 0 {
		v.res.Checkpoint = &final
	}
	return v.res, nil
}

// checkResumePoint verifica la fila del punto con un verificador propio. Si falla, deja el
// veredicto en v: el contenido de esa fila no cuadra con su hash, no tiene el hash registrado, o ya
// no existe.
func checkResumePoint(ctx context.Context, pool *db.ContextPool, v *chainVerifier, at string, scan rowScanner, cp *domain.ChainCheckpoint,
	drain func(pgx.Rows, *chainVerifier) (int, bool, error)) error {
	probe := newChainVerifier(v.res.Chain, v.ring, v.tenantID)
	probe.adoptPrev = true
	rows, err := pool.Query(ctx, at, cp.Seq)
	if err != nil {
		return err
	}
	n, _, err := drain(rows, probe)
	if err != nil {
		return err
	}
	switch {
	case n == 0 || (probe.res.OK && (probe.lastVersion != cp.HashVersion || !hashesEqual(probe.prev, cp.Hash))):
		v.failCheckpoint(cp)
	case !probe.res.OK:
		v.res.OK, v.res.Reason, v.res.Head = false, probe.res.Reason, nil
		v.res.BrokenID, v.res.BrokenSeq, v.res.BrokenVersion = probe.res.BrokenID, probe.res.BrokenSeq, probe.res.BrokenVersion
	}
	return nil
}
