package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// FirewallUseCase administra el cortafuegos de la celda (netfilter): redes permitidas y
// denegadas, opciones de baneo y desbaneos. Protege a todas las empresas de la celda, asi
// que solo lo opera el superadmin: el permiso firewall es de plataforma y aqui se exige de
// nuevo, porque la comprobacion de permisos deja pasar tambien al administrador de una
// empresa. Sus tablas no tienen empresa: corre como duena del pool.
type FirewallUseCase struct {
	tx     ports.OwnerTransactor
	repo   ports.FirewallRepository
	policy ports.PolicyReader
	sync   *RedisSync
	store  ports.EngineStore
	logger *zap.Logger
}

type FirewallDeps struct {
	Tx     ports.OwnerTransactor
	Repo   ports.FirewallRepository
	Policy ports.PolicyReader
	Sync   *RedisSync
	Store  ports.EngineStore
	Logger *zap.Logger
}

func NewFirewallUseCase(d FirewallDeps) *FirewallUseCase {
	return &FirewallUseCase{tx: d.Tx, repo: d.Repo, policy: d.Policy, sync: d.Sync, store: d.Store, logger: d.Logger}
}

func (uc *FirewallUseCase) ListNetworks(ctx context.Context, platform bool) ([]domain.FirewallNetwork, error) {
	if !platform {
		return nil, domain.ErrPlatformOnly
	}
	return uc.policy.AllFirewallNetworks(ctx)
}

// AddNetwork anade la red a la lista; una red solo puede estar en una de las dos.
func (uc *FirewallUseCase) AddNetwork(ctx context.Context, platform bool, list domain.FirewallList, network, note string) (*domain.FirewallNetwork, error) {
	if !platform {
		return nil, domain.ErrPlatformOnly
	}
	if err := domain.ValidateFirewallList(list); err != nil {
		return nil, err
	}
	net, err := domain.NormalizeFirewallNetwork(network)
	if err != nil {
		return nil, err
	}
	n := &domain.FirewallNetwork{List: list, Network: net, Note: strings.TrimSpace(note)}
	if err := uc.tx.Transact(ctx, func(ctx context.Context) error { return uc.repo.CreateFirewallNetwork(ctx, n) }); err != nil {
		return nil, err
	}
	logAfterCommit(ctx, uc.logger, list.RedisKey(), func(ctx context.Context) error { return uc.sync.SyncFirewallNetwork(ctx, *n) })
	return n, nil
}

func (uc *FirewallUseCase) DeleteNetwork(ctx context.Context, platform bool, id uuid.UUID) error {
	if !platform {
		return domain.ErrPlatformOnly
	}
	var removed *domain.FirewallNetwork
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		var err error
		removed, err = uc.repo.DeleteFirewallNetwork(ctx, id)
		return err
	})
	if err != nil {
		return err
	}
	logAfterCommit(ctx, uc.logger, removed.List.RedisKey(), func(ctx context.Context) error { return uc.sync.RemoveFirewallNetwork(ctx, *removed) })
	return nil
}

// Options devuelve las opciones fijadas o, sin ellas, las que usa netfilter por defecto.
func (uc *FirewallUseCase) Options(ctx context.Context, platform bool) (*domain.FirewallOptions, error) {
	if !platform {
		return nil, domain.ErrPlatformOnly
	}
	o, err := uc.policy.FirewallOptions(ctx)
	if err == domain.ErrNotFound {
		def := domain.DefaultFirewallOptions()
		return &def, nil
	}
	return o, err
}

func (uc *FirewallUseCase) PutOptions(ctx context.Context, platform bool, o domain.FirewallOptions) (*domain.FirewallOptions, error) {
	if !platform {
		return nil, domain.ErrPlatformOnly
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if err := uc.tx.Transact(ctx, func(ctx context.Context) error { return uc.repo.UpsertFirewallOptions(ctx, &o) }); err != nil {
		return nil, err
	}
	logAfterCommit(ctx, uc.logger, domain.RedisF2BOptions, func(ctx context.Context) error { return uc.sync.SyncFirewallOptions(ctx, o) })
	return &o, nil
}

// Bans lista los baneos vigentes tal como los lleva netfilter en Redis.
func (uc *FirewallUseCase) Bans(ctx context.Context, platform bool) ([]domain.FirewallBan, error) {
	if !platform {
		return nil, domain.ErrPlatformOnly
	}
	if err := uc.store.Ping(ctx); err != nil {
		return nil, domain.ErrRedisUnavailable
	}
	active, err := uc.store.HGetAll(ctx, domain.RedisF2BActiveBans)
	if err != nil {
		return nil, err
	}
	permanent, err := uc.store.HGetAll(ctx, domain.RedisF2BPermBans)
	if err != nil {
		return nil, err
	}
	return domain.ParseFirewallBans(active, permanent), nil
}

// Unban pide a netfilter que levante un baneo temporal vigente (lo procesa en su
// siguiente vuelta). Un baneo permanente sale de la lista de denegadas, no de aqui.
func (uc *FirewallUseCase) Unban(ctx context.Context, platform bool, network string) error {
	if !platform {
		return domain.ErrPlatformOnly
	}
	net, err := domain.NormalizeBannedNetwork(network)
	if err != nil {
		return err
	}
	if err := uc.store.Ping(ctx); err != nil {
		return domain.ErrRedisUnavailable
	}
	if _, banned, err := uc.store.HGet(ctx, domain.RedisF2BActiveBans, net); err != nil {
		return err
	} else if !banned {
		return domain.ErrNotFound
	}
	return uc.store.HSet(ctx, domain.RedisF2BQueueUnban, net, "1")
}
