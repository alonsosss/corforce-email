package app

import (
	"context"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// DKIMUseCase custodia las claves DKIM del Redis de los motores de la celda con una regla: solo
// hay claves de dominios activos en el directorio de esta celda (mail.v_routing_domains) cuya
// empresa sigue existiendo. Se aplica al escribir (domain-service no deposita la clave de un
// dominio que la celda no sirve), en cada evento del directorio sobre un dominio (desactivado o
// borrado, se retiran) y en un repaso periodico que retira lo que quedo por un fallo entre pasos
// o por la baja de una empresa. Toda decision se toma con el cerrojo del dominio
// (ports.DKIMDomainLock) y leyendo el directorio dentro de el.
type DKIMUseCase struct {
	dir     ports.DirectoryReader
	lock    ports.DKIMDomainLock
	sync    *RedisSync
	tenants ports.TenantRegistry
	logger  *zap.Logger
}

type DKIMDeps struct {
	Directory ports.DirectoryReader
	Lock      ports.DKIMDomainLock
	Sync      *RedisSync
	Tenants   ports.TenantRegistry
	Logger    *zap.Logger
}

func NewDKIMUseCase(d DKIMDeps) *DKIMUseCase {
	uc := &DKIMUseCase{dir: d.Directory, lock: d.Lock, sync: d.Sync, tenants: d.Tenants, logger: d.Logger}
	if uc.logger == nil {
		uc.logger = zap.NewNop()
	}
	return uc
}

// Motivos por los que un dominio pierde sus claves en el repaso.
const (
	dkimReasonNotServed  = "not_served"
	dkimReasonTenantGone = "tenant_gone"
)

// PutKeys deja en los motores exactamente el juego de claves del dominio, en orden (la ultima
// firma): los selectores que no vienen se retiran. Solo de un dominio activo de la empresa.
func (uc *DKIMUseCase) PutKeys(ctx context.Context, tenantID uuid.UUID, domainName string, keys []domain.DKIMKey) error {
	name, err := domain.NormalizeDKIMDomain(domainName)
	if err != nil {
		return err
	}
	set, err := domain.NormalizeDKIMKeySet(name, keys)
	if err != nil {
		return err
	}
	return uc.lock.WithDomainLock(ctx, name, func(ctx context.Context) error {
		if err := uc.servedFor(ctx, tenantID, name); err != nil {
			return err
		}
		return uc.sync.SyncDKIMSet(ctx, name, set)
	})
}

// PutKey publica una clave y la deja como la que firma, conservando las demas del dominio. Es
// la forma anterior del contrato (una clave por llamada), con la misma regla que PutKeys.
func (uc *DKIMUseCase) PutKey(ctx context.Context, tenantID uuid.UUID, k domain.DKIMKey) error {
	name, err := domain.NormalizeDKIMDomain(k.Domain)
	if err != nil {
		return err
	}
	if k, err = domain.NormalizeDKIMKey(k); err != nil {
		return err
	}
	k.Domain = name
	return uc.lock.WithDomainLock(ctx, name, func(ctx context.Context) error {
		if err := uc.servedFor(ctx, tenantID, name); err != nil {
			return err
		}
		return uc.sync.SyncDKIM(ctx, k)
	})
}

// DeleteSelector retira una clave concreta (fin de la gracia de una rotacion).
func (uc *DKIMUseCase) DeleteSelector(ctx context.Context, tenantID uuid.UUID, domainName, selector string) error {
	name, err := domain.NormalizeDKIMDomain(domainName)
	if err != nil {
		return err
	}
	selector = strings.ToLower(strings.TrimSpace(selector))
	if err := domain.ValidateDKIMSelector(selector); err != nil {
		return err
	}
	return uc.lock.WithDomainLock(ctx, name, func(ctx context.Context) error {
		if err := uc.notForeign(ctx, tenantID, name); err != nil {
			return err
		}
		return uc.sync.RemoveDKIMSelector(ctx, name, selector)
	})
}

// DeleteDomain retira todas las claves del dominio. Retirar lo que no esta no es un error.
func (uc *DKIMUseCase) DeleteDomain(ctx context.Context, tenantID uuid.UUID, domainName string) error {
	name, err := domain.NormalizeDKIMDomain(domainName)
	if err != nil {
		return err
	}
	return uc.lock.WithDomainLock(ctx, name, func(ctx context.Context) error {
		if err := uc.notForeign(ctx, tenantID, name); err != nil {
			return err
		}
		_, err := uc.sync.RemoveDKIMDomain(ctx, name)
		return err
	})
}

// ForgetIfNotServed retira las claves de un dominio que el directorio ya no sirve (desactivado o
// borrado). Lo llama el consumidor de los eventos del directorio; es idempotente y lee el estado
// real, no el del evento. Un nombre que no es un dominio no puede tener claves.
func (uc *DKIMUseCase) ForgetIfNotServed(ctx context.Context, domainName string) error {
	name, err := domain.NormalizeDKIMDomain(domainName)
	if err != nil {
		return nil
	}
	return uc.lock.WithDomainLock(ctx, name, func(ctx context.Context) error {
		st, found, err := uc.state(ctx, name)
		if err != nil || (found && st.Active) {
			return err
		}
		removed, err := uc.sync.RemoveDKIMDomain(ctx, name)
		if err == nil && removed > 0 {
			uc.logger.Info("claves DKIM retiradas: la celda ya no sirve el dominio",
				zap.String("domain", name), zap.Int("claves", removed))
		}
		return err
	})
}

// DKIMReconcileReport resume una pasada del repaso.
type DKIMReconcileReport struct {
	// Domains son los dominios con claves en los motores al empezar.
	Domains int
	// NotServed y TenantGone son los dominios cuyas claves se retiraron, por motivo.
	NotServed  int
	TenantGone int
	// Unresolved son los dominios que se conservan porque organization no respondio.
	Unresolved int
}

type tenantAnswer struct {
	gone bool
	err  error
}

// Reconcile retira las claves de los dominios que la celda no sirve o cuya empresa ya no existe.
// Lo que encuentra es un camino que fallo (una caida entre dos pasos, una baja de empresa que no
// desactivo sus dominios): se registra cada uno. Sin respuesta de organization no se decide nada
// sobre esa empresa.
func (uc *DKIMUseCase) Reconcile(ctx context.Context) (DKIMReconcileReport, error) {
	var rep DKIMReconcileReport
	names, err := uc.sync.DKIMDomains(ctx)
	if err != nil {
		return rep, err
	}
	rep.Domains = len(names)
	if len(names) == 0 {
		return rep, nil
	}
	states, err := uc.dir.DomainStates(ctx, names)
	if err != nil {
		return rep, err
	}
	answers := map[uuid.UUID]tenantAnswer{}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if domain.ValidateDomainName(name) != nil {
			uc.logger.Warn("repaso DKIM: campo con un dominio que no lo es; se ignora", zap.String("domain", name))
			continue
		}
		seen, found := states[name]
		reason := dkimReasonNotServed
		if found && seen.Active {
			ans, asked := answers[seen.TenantID]
			if !asked {
				ans.gone, ans.err = uc.tenants.TenantGone(ctx, seen.TenantID)
				answers[seen.TenantID] = ans
				if ans.err != nil {
					uc.logger.Warn("repaso DKIM: organization no dio la empresa del dominio; se conservan sus claves",
						zap.String("tenant_id", seen.TenantID.String()), zap.Error(ans.err))
				}
			}
			if ans.err != nil {
				rep.Unresolved++
				continue
			}
			if !ans.gone {
				continue
			}
			reason = dkimReasonTenantGone
		}
		removed, err := uc.forgetUnchanged(ctx, name, seen.TenantID, reason)
		if err != nil {
			return rep, err
		}
		if !removed {
			continue
		}
		if reason == dkimReasonTenantGone {
			rep.TenantGone++
		} else {
			rep.NotServed++
		}
		fields := []zap.Field{zap.String("domain", name), zap.String("motivo", reason)}
		if found {
			fields = append(fields, zap.String("tenant_id", seen.TenantID.String()))
		}
		uc.logger.Warn("repaso DKIM: claves retiradas de un dominio que la celda ya no debia firmar", fields...)
	}
	return rep, nil
}

// forgetUnchanged retira las claves del dominio si, con su cerrojo tomado, el motivo sigue en pie:
// un dominio que se activo entretanto conserva las que acaba de recibir, y uno que cambio de
// empresa se decide en la pasada siguiente.
func (uc *DKIMUseCase) forgetUnchanged(ctx context.Context, name string, tenantID uuid.UUID, reason string) (bool, error) {
	removed := false
	err := uc.lock.WithDomainLock(ctx, name, func(ctx context.Context) error {
		st, found, err := uc.state(ctx, name)
		if err != nil {
			return err
		}
		stillServed := found && st.Active
		if stillServed && (reason != dkimReasonTenantGone || st.TenantID != tenantID) {
			return nil
		}
		if _, err := uc.sync.RemoveDKIMDomain(ctx, name); err != nil {
			return err
		}
		removed = true
		return nil
	})
	return removed, err
}

// RunReconciler repasa al arrancar y cada interval, solo en la replica que toma el cerrojo de
// lider. Bloquea hasta que el contexto se cancele; el contexto lleva el pool de la celda.
func (uc *DKIMUseCase) RunReconciler(ctx context.Context, interval time.Duration, acquire func(context.Context) (release func(), ok bool)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		uc.reconcileTick(ctx, interval, acquire)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (uc *DKIMUseCase) reconcileTick(ctx context.Context, interval time.Duration, acquire func(context.Context) (func(), bool)) {
	release, ok := acquire(ctx)
	if !ok {
		return
	}
	defer release()
	tctx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()
	rep, err := uc.Reconcile(tctx)
	if err != nil && tctx.Err() == nil {
		uc.logger.Warn("repaso DKIM incompleto; se repite en el siguiente", zap.Error(err))
	}
	if rep.NotServed+rep.TenantGone+rep.Unresolved > 0 {
		uc.logger.Info("repaso DKIM", zap.Int("dominios", rep.Domains), zap.Int("sin_servir", rep.NotServed),
			zap.Int("empresa_retirada", rep.TenantGone), zap.Int("sin_resolver", rep.Unresolved))
	}
}

// state lee el dominio en el directorio de la celda.
func (uc *DKIMUseCase) state(ctx context.Context, name string) (domain.DirectoryDomain, bool, error) {
	states, err := uc.dir.DomainStates(ctx, []string{name})
	if err != nil {
		return domain.DirectoryDomain{}, false, err
	}
	st, found := states[name]
	return st, found, nil
}

// servedFor exige que el dominio sea de la empresa y este activo en el directorio de la celda.
// El de otra empresa es ErrObjectNotOwned aunque este inactivo.
func (uc *DKIMUseCase) servedFor(ctx context.Context, tenantID uuid.UUID, name string) error {
	st, found, err := uc.state(ctx, name)
	switch {
	case err != nil:
		return err
	case found && st.TenantID != tenantID:
		return domain.ErrObjectNotOwned
	case !found || !st.Active:
		return domain.ErrDKIMDomainNotActive
	}
	return nil
}

// notForeign deja retirar las claves de un dominio de la empresa o que ya no esta en el
// directorio, nunca las de un dominio de otra empresa.
func (uc *DKIMUseCase) notForeign(ctx context.Context, tenantID uuid.UUID, name string) error {
	st, found, err := uc.state(ctx, name)
	if err != nil {
		return err
	}
	if found && st.TenantID != tenantID {
		return domain.ErrObjectNotOwned
	}
	return nil
}
