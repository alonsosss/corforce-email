package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

// DeliveryLog implementa ports.DeliveryLogSource sobre el Redis de los motores. syslog-ng hace LPUSH
// en POSTFIX_DELIVERY_LOG, asi que la linea mas antigua esta a la derecha: BLMOVE RIGHT LEFT la pasa
// a la lista de trabajo en orden y sin perderla si el servicio cae antes de guardarla.
type DeliveryLog struct {
	client *goredis.Client
}

func NewDeliveryLog(s *Store) *DeliveryLog { return &DeliveryLog{client: s.client} }

func (d *DeliveryLog) Next(ctx context.Context, wait time.Duration) (string, bool, error) {
	v, err := d.client.BLMove(ctx, domain.RedisDeliveryLog, domain.RedisDeliveryLogWork, "RIGHT", "LEFT", wait).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Pending lee la lista de trabajo de derecha a izquierda: la primera es la mas antigua.
func (d *DeliveryLog) Pending(ctx context.Context) ([]string, error) {
	items, err := d.client.LRange(ctx, domain.RedisDeliveryLogWork, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

// Ack quita una aparicion de la linea, empezando por la mas antigua (la derecha).
func (d *DeliveryLog) Ack(ctx context.Context, raw string) error {
	return d.client.LRem(ctx, domain.RedisDeliveryLogWork, -1, raw).Err()
}

func (d *DeliveryLog) Backlog(ctx context.Context) (int64, error) {
	return d.client.LLen(ctx, domain.RedisDeliveryLog).Result()
}

func (d *DeliveryLog) LoadContext(ctx context.Context, qid string) (domain.QueueContext, error) {
	var qc domain.QueueContext
	v, err := d.client.Get(ctx, domain.RedisDeliveryQIDPrefix+qid).Result()
	if errors.Is(err, goredis.Nil) {
		return qc, nil
	}
	if err != nil {
		return qc, err
	}
	if err := json.Unmarshal([]byte(v), &qc); err != nil {
		// Un contexto ilegible no impide registrar la entrega: sale sin el.
		return domain.QueueContext{}, nil
	}
	return qc, nil
}

func (d *DeliveryLog) SaveContext(ctx context.Context, qid string, qc domain.QueueContext, ttl time.Duration) error {
	b, err := json.Marshal(qc)
	if err != nil {
		return err
	}
	return d.client.Set(ctx, domain.RedisDeliveryQIDPrefix+qid, b, ttl).Err()
}
