// Package redis lleva los limites de tasa por hora y por dia de cada empresa y clase en el
// Redis de la plataforma.
package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	keyPrefix = "rep:"
	// Cada clave vive mas que su ventana para que el uso de la hora o el dia en curso se
	// pueda leer hasta el final, y caduca sola: no hay nada que limpiar.
	hourTTL = 2 * time.Hour
	dayTTL  = 48 * time.Hour
)

// reserveScript comprueba las dos ventanas y solo si ambas tienen cupo incrementa las
// dos, todo dentro de Redis: dos peticiones simultaneas no pueden llevarse las dos el
// ultimo hueco. El dia se comprueba antes que la hora para que, si faltan los dos, se
// informe la espera mas larga. El TTL solo se fija si la clave no lo tiene, de modo que
// la caducidad no se renueve con cada envio.
//
// KEYS: 1 hora, 2 dia. ARGV: 1 cantidad, 2 limite por hora, 3 limite por dia, 4 TTL de la
// hora en segundos, 5 TTL del dia en segundos.
// Devuelve {permitido 1|0, usado en la hora, usado en el dia, ventana agotada 0|1 hora|2 dia}.
var reserveScript = goredis.NewScript(`
local n = tonumber(ARGV[1])
local hour = tonumber(redis.call('GET', KEYS[1]) or '0')
local day = tonumber(redis.call('GET', KEYS[2]) or '0')
if day + n > tonumber(ARGV[3]) then
  return {0, hour, day, 2}
end
if hour + n > tonumber(ARGV[2]) then
  return {0, hour, day, 1}
end
hour = redis.call('INCRBY', KEYS[1], n)
if redis.call('TTL', KEYS[1]) < 0 then
  redis.call('EXPIRE', KEYS[1], ARGV[4])
end
day = redis.call('INCRBY', KEYS[2], n)
if redis.call('TTL', KEYS[2]) < 0 then
  redis.call('EXPIRE', KEYS[2], ARGV[5])
end
return {1, hour, day, 0}
`)

// RateLimiter implementa ports.RateLimiter.
type RateLimiter struct {
	rdb goredis.UniversalClient
}

func NewRateLimiter(rdb goredis.UniversalClient) *RateLimiter {
	return &RateLimiter{rdb: rdb}
}

// HourKey es la clave del uso de la hora UTC en curso.
func HourKey(tenantID uuid.UUID, class domain.Class, now time.Time) string {
	return keyPrefix + tenantID.String() + ":" + string(class) + ":h:" + now.UTC().Format("2006010215")
}

// DayKey es la clave del uso del dia UTC en curso.
func DayKey(tenantID uuid.UUID, class domain.Class, now time.Time) string {
	return keyPrefix + tenantID.String() + ":" + string(class) + ":d:" + now.UTC().Format("20060102")
}

func (l *RateLimiter) Reserve(ctx context.Context, tenantID uuid.UUID, class domain.Class, now time.Time, count int64, limits domain.Limits) (domain.RateOutcome, error) {
	res, err := reserveScript.Run(ctx, l.rdb,
		[]string{HourKey(tenantID, class, now), DayKey(tenantID, class, now)},
		count, limits.Hourly, limits.Daily, int64(hourTTL/time.Second), int64(dayTTL/time.Second),
	).Int64Slice()
	if err != nil {
		return domain.RateOutcome{}, err
	}
	if len(res) != 4 {
		return domain.RateOutcome{}, fmt.Errorf("respuesta inesperada del script de tasa: %v", res)
	}
	out := domain.RateOutcome{Allowed: res[0] == 1, HourUsed: res[1], DayUsed: res[2]}
	switch res[3] {
	case 1:
		out.Exceeded = domain.WindowHour
	case 2:
		out.Exceeded = domain.WindowDay
	}
	return out, nil
}

func (l *RateLimiter) Usage(ctx context.Context, tenantID uuid.UUID, class domain.Class, now time.Time) (int64, int64, error) {
	vals, err := l.rdb.MGet(ctx, HourKey(tenantID, class, now), DayKey(tenantID, class, now)).Result()
	if err != nil {
		return 0, 0, err
	}
	if len(vals) != 2 {
		return 0, 0, fmt.Errorf("respuesta inesperada de MGET: %d valores", len(vals))
	}
	hour, err := counterValue(vals[0])
	if err != nil {
		return 0, 0, err
	}
	day, err := counterValue(vals[1])
	if err != nil {
		return 0, 0, err
	}
	return hour, day, nil
}

// counterValue lee un contador de MGET: una clave que no existe es cero.
func counterValue(v interface{}) (int64, error) {
	switch s := v.(type) {
	case nil:
		return 0, nil
	case string:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("contador de tasa ilegible %q: %w", s, err)
		}
		return n, nil
	}
	return 0, fmt.Errorf("contador de tasa de tipo inesperado %T", v)
}
