package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// RedisSync es el unico escritor de las claves de Redis que los motores leen. Cada
// cambio por API escribe su clave al momento; la reconciliacion periodica recalcula
// todo desde la base para que un Redis vaciado se recupere solo.
type RedisSync struct {
	store  ports.EngineStore
	dir    ports.DirectoryReader
	policy ports.PolicyReader
	logger *zap.Logger
}

func NewRedisSync(store ports.EngineStore, dir ports.DirectoryReader, policy ports.PolicyReader, logger *zap.Logger) *RedisSync {
	return &RedisSync{store: store, dir: dir, policy: policy, logger: logger}
}

// ReconcileAll recalcula todas las claves derivadas de la base. Las claves DKIM no se
// reconstruyen: este servicio no guarda las claves privadas, las publica domain-service; las
// que sobran las retira el repaso de DKIMUseCase.
func (s *RedisSync) ReconcileAll(ctx context.Context) error {
	var firstErr error
	keep := func(err error, what string) {
		if err != nil {
			s.logger.Error("reconciliacion de redis", zap.String("clave", what), zap.Error(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	keep(s.ReconcileDomains(ctx), domain.RedisDomainMap)
	keep(s.reconcileRateLimits(ctx), domain.RedisRateLimitValue)
	keep(s.reconcileForwardingHosts(ctx), domain.RedisWhitelistedFwdHost)
	keep(s.reconcileMailboxTags(ctx), domain.RedisWantsSubjectTag)
	keep(s.reconcileSMTPAccess(ctx), domain.RedisSMTPLimitedAccess)
	keep(s.reconcileFirewall(ctx), domain.RedisF2BWhitelist)
	keep(s.SyncQuarantineTop(ctx), domain.RedisQuarantineMaxSize)
	return firstErr
}

// SyncSMTPAccess publica las redes del buzon y DESPUES lo marca como limitado: al reves,
// durante un instante Rspamd veria al usuario limitado sin redes y rechazaria su envio.
func (s *RedisSync) SyncSMTPAccess(ctx context.Context, a domain.SMTPAccess) error {
	want := make(map[string]string, len(a.Networks))
	for _, n := range a.Networks {
		want[n] = "1"
	}
	if err := s.reconcileHash(ctx, domain.SMTPAllowNetsKey(a.Username), want); err != nil {
		return err
	}
	return s.store.HSet(ctx, domain.RedisSMTPLimitedAccess, a.Username, "1")
}

// RemoveSMTPAccess retira la marca ANTES que las redes, por el mismo motivo.
func (s *RedisSync) RemoveSMTPAccess(ctx context.Context, username string) error {
	if err := s.store.HDel(ctx, domain.RedisSMTPLimitedAccess, username); err != nil {
		return err
	}
	return s.store.Del(ctx, domain.SMTPAllowNetsKey(username))
}

func (s *RedisSync) reconcileSMTPAccess(ctx context.Context) error {
	all, err := s.policy.AllSMTPAccess(ctx)
	if err != nil {
		return err
	}
	limited := make(map[string]string, len(all))
	keep := make(map[string]bool, len(all))
	for _, a := range all {
		want := make(map[string]string, len(a.Networks))
		for _, n := range a.Networks {
			want[n] = "1"
		}
		key := domain.SMTPAllowNetsKey(a.Username)
		if err := s.reconcileHash(ctx, key, want); err != nil {
			return err
		}
		limited[a.Username] = "1"
		keep[key] = true
	}
	if err := s.reconcileHash(ctx, domain.RedisSMTPLimitedAccess, limited); err != nil {
		return err
	}
	keys, err := s.store.Keys(ctx, domain.RedisSMTPAllowNetsPrefix+"*")
	if err != nil {
		return err
	}
	var stale []string
	for _, k := range keys {
		if !keep[k] {
			stale = append(stale, k)
		}
	}
	return s.store.Del(ctx, stale...)
}

// SyncFirewallNetwork pone la red en su lista de netfilter y la quita de la otra.
func (s *RedisSync) SyncFirewallNetwork(ctx context.Context, n domain.FirewallNetwork) error {
	other := domain.FirewallDeny
	if n.List == domain.FirewallDeny {
		other = domain.FirewallAllow
	}
	if err := s.store.HDel(ctx, other.RedisKey(), n.Network); err != nil {
		return err
	}
	return s.store.HSet(ctx, n.List.RedisKey(), n.Network, "1")
}

func (s *RedisSync) RemoveFirewallNetwork(ctx context.Context, n domain.FirewallNetwork) error {
	return s.store.HDel(ctx, n.List.RedisKey(), n.Network)
}

// SyncFirewallOptions escribe en F2B_OPTIONS las claves que gobierna la plataforma y
// conserva las que mantiene netfilter (banlist_id, manage_external). netfilter relee la
// clave cada pocos segundos.
func (s *RedisSync) SyncFirewallOptions(ctx context.Context, o domain.FirewallOptions) error {
	current := map[string]interface{}{}
	raw, ok, err := s.store.Get(ctx, domain.RedisF2BOptions)
	if err != nil {
		return err
	}
	// Un valor que no es JSON se reemplaza: con el, netfilter se detiene al arrancar.
	if ok && json.Unmarshal([]byte(raw), &current) != nil {
		current = map[string]interface{}{}
	}
	for k, v := range o.Managed() {
		current[k] = v
	}
	data, err := json.Marshal(current)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, domain.RedisF2BOptions, string(data))
}

// reconcileFirewall deja las listas de netfilter como la base. Sin opciones propias de la
// plataforma, F2B_OPTIONS se deja a los valores por defecto de netfilter.
func (s *RedisSync) reconcileFirewall(ctx context.Context) error {
	nets, err := s.policy.AllFirewallNetworks(ctx)
	if err != nil {
		return err
	}
	allow, deny := map[string]string{}, map[string]string{}
	for _, n := range nets {
		if n.List == domain.FirewallDeny {
			deny[n.Network] = "1"
		} else {
			allow[n.Network] = "1"
		}
	}
	if err := s.reconcileHash(ctx, domain.RedisF2BWhitelist, allow); err != nil {
		return err
	}
	if err := s.reconcileHash(ctx, domain.RedisF2BBlacklist, deny); err != nil {
		return err
	}
	opts, err := s.policy.FirewallOptions(ctx)
	if err == domain.ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	return s.SyncFirewallOptions(ctx, *opts)
}

// ReconcileDomains deja DOMAIN_MAP exactamente igual a los dominios y dominios alias
// activos de la celda.
func (s *RedisSync) ReconcileDomains(ctx context.Context) error {
	domains, err := s.dir.ActiveDomains(ctx)
	if err != nil {
		return fmt.Errorf("dominios activos: %w", err)
	}
	want := make(map[string]string, len(domains))
	for _, d := range domains {
		want[d] = "1"
	}
	return s.reconcileHash(ctx, domain.RedisDomainMap, want)
}

// RefreshDomain actualiza un dominio (o dominio alias) tras un evento del directorio y,
// en cascada, los dominios alias que apuntan a el: desactivar un dominio deja sin
// destino a sus alias. Se lee el estado real de las vistas y no el del evento, asi que
// es idempotente y no depende de que el payload lleve el campo active.
func (s *RedisSync) RefreshDomain(ctx context.Context, domainName string) error {
	aliases, err := s.dir.AliasDomainsOf(ctx, domainName)
	if err != nil {
		return err
	}
	for _, d := range append([]string{domainName}, aliases...) {
		active, err := s.dir.DomainActive(ctx, d)
		if err != nil {
			return err
		}
		if active {
			err = s.store.HSet(ctx, domain.RedisDomainMap, d, "1")
		} else {
			err = s.store.HDel(ctx, domain.RedisDomainMap, d)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *RedisSync) SyncRateLimit(ctx context.Context, r domain.RateLimit) error {
	return s.store.HSet(ctx, domain.RedisRateLimitValue, r.Object, r.Value)
}

func (s *RedisSync) RemoveRateLimit(ctx context.Context, object string) error {
	return s.store.HDel(ctx, domain.RedisRateLimitValue, object)
}

func (s *RedisSync) reconcileRateLimits(ctx context.Context) error {
	all, err := s.policy.AllRateLimits(ctx)
	if err != nil {
		return err
	}
	want := make(map[string]string, len(all))
	for _, r := range all {
		want[r.Object] = r.Value
	}
	return s.reconcileHash(ctx, domain.RedisRateLimitValue, want)
}

// SyncForwardingHost publica el host de confianza y, si su spam no se filtra, lo anade
// a KEEP_SPAM (Rspamd acepta sin analizar lo que venga de ahi).
func (s *RedisSync) SyncForwardingHost(ctx context.Context, h domain.ForwardingHost) error {
	if err := s.store.HSet(ctx, domain.RedisWhitelistedFwdHost, h.Host, h.Source); err != nil {
		return err
	}
	if h.FilterSpam {
		return s.store.HDel(ctx, domain.RedisKeepSpam, h.Host)
	}
	return s.store.HSet(ctx, domain.RedisKeepSpam, h.Host, "1")
}

func (s *RedisSync) RemoveForwardingHost(ctx context.Context, host string) error {
	if err := s.store.HDel(ctx, domain.RedisWhitelistedFwdHost, host); err != nil {
		return err
	}
	return s.store.HDel(ctx, domain.RedisKeepSpam, host)
}

func (s *RedisSync) reconcileForwardingHosts(ctx context.Context) error {
	all, err := s.policy.AllForwardingHosts(ctx)
	if err != nil {
		return err
	}
	hosts := make(map[string]string, len(all))
	keep := map[string]string{}
	for _, h := range all {
		hosts[h.Host] = h.Source
		if !h.FilterSpam {
			keep[h.Host] = "1"
		}
	}
	if err := s.reconcileHash(ctx, domain.RedisWhitelistedFwdHost, hosts); err != nil {
		return err
	}
	return s.reconcileHash(ctx, domain.RedisKeepSpam, keep)
}

func (s *RedisSync) SyncMailboxTags(ctx context.Context, t domain.MailboxTags) error {
	if err := s.syncFlag(ctx, domain.RedisWantsSubjectTag, t.Username, t.SubjectTag); err != nil {
		return err
	}
	return s.syncFlag(ctx, domain.RedisWantsSubfolderTag, t.Username, t.SubfolderTag)
}

func (s *RedisSync) RemoveMailboxTags(ctx context.Context, username string) error {
	if err := s.store.HDel(ctx, domain.RedisWantsSubjectTag, username); err != nil {
		return err
	}
	return s.store.HDel(ctx, domain.RedisWantsSubfolderTag, username)
}

func (s *RedisSync) reconcileMailboxTags(ctx context.Context) error {
	all, err := s.policy.AllMailboxTags(ctx)
	if err != nil {
		return err
	}
	subject, subfolder := map[string]string{}, map[string]string{}
	for _, t := range all {
		if t.SubjectTag {
			subject[t.Username] = "1"
		}
		if t.SubfolderTag {
			subfolder[t.Username] = "1"
		}
	}
	if err := s.reconcileHash(ctx, domain.RedisWantsSubjectTag, subject); err != nil {
		return err
	}
	return s.reconcileHash(ctx, domain.RedisWantsSubfolderTag, subfolder)
}

// SyncQuarantineTop escribe en Q_* el tope de la celda (maximo entre empresas y union
// de dominios excluidos). Los limites por empresa los aplica /pipe con su propia fila.
func (s *RedisSync) SyncQuarantineTop(ctx context.Context) error {
	all, err := s.policy.AllQuarantineSettings(ctx)
	if err != nil {
		return err
	}
	top := domain.ComputeCellQuarantineTop(all)
	exclude, err := json.Marshal(top.ExcludeDomains)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		domain.RedisQuarantineMaxSize: strconv.FormatInt(top.MaxSizeMiB, 10),
		domain.RedisQuarantineMaxAge:  strconv.Itoa(top.MaxAgeDays),
		domain.RedisQuarantineRetain:  strconv.Itoa(top.RetentionSize),
		domain.RedisQuarantineExclude: string(exclude),
	} {
		if err := s.store.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

// SyncDKIM publica la clave privada y deja el selector activo apuntando a ella. Las
// claves de otros selectores del mismo dominio se conservan: durante una rotacion
// conviven dos hasta que domain-service retira la vieja. La clave solo pasa por aqui:
// no se guarda en la base de este servicio.
func (s *RedisSync) SyncDKIM(ctx context.Context, k domain.DKIMKey) error {
	if err := s.store.HSet(ctx, domain.RedisDKIMPrivKeys, domain.DKIMKeyField(k.Selector, k.Domain), k.PrivateKeyPEM); err != nil {
		return err
	}
	return s.store.HSet(ctx, domain.RedisDKIMSelectors, k.Domain, k.Selector)
}

// SyncDKIMSet deja en los motores exactamente el juego de claves del dominio: escribe todas, el
// selector activo pasa a la ultima y solo despues retira las demas del dominio, de modo que
// Rspamd nunca ve un selector activo sin su clave.
func (s *RedisSync) SyncDKIMSet(ctx context.Context, domainName string, keys []domain.DKIMKey) error {
	if len(keys) == 0 {
		return fmt.Errorf("juego DKIM de %s sin claves", domainName)
	}
	keep := make(map[string]bool, len(keys))
	for _, k := range keys {
		field := domain.DKIMKeyField(k.Selector, domainName)
		if err := s.store.HSet(ctx, domain.RedisDKIMPrivKeys, field, k.PrivateKeyPEM); err != nil {
			return err
		}
		keep[field] = true
	}
	if err := s.store.HSet(ctx, domain.RedisDKIMSelectors, domainName, keys[len(keys)-1].Selector); err != nil {
		return err
	}
	fields, err := s.dkimFieldsOf(ctx, domainName)
	if err != nil {
		return err
	}
	var stale []string
	for _, f := range fields {
		if !keep[f] {
			stale = append(stale, f)
		}
	}
	return s.store.HDel(ctx, domain.RedisDKIMPrivKeys, stale...)
}

// DKIMDomains lista, ordenados, los dominios con alguna clave o selector DKIM en los motores.
func (s *RedisSync) DKIMDomains(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	fields, err := s.store.HKeys(ctx, domain.RedisDKIMPrivKeys, "*")
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if _, d, ok := domain.SplitDKIMKeyField(f); ok {
			seen[d] = true
		}
	}
	selectors, err := s.store.HKeys(ctx, domain.RedisDKIMSelectors, "*")
	if err != nil {
		return nil, err
	}
	for _, d := range selectors {
		seen[d] = true
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// RemoveDKIMSelector retira solo esa clave; el selector activo se retira si apuntaba a
// ella (Rspamd deja de firmar el dominio hasta que se publique otra).
func (s *RedisSync) RemoveDKIMSelector(ctx context.Context, domainName, selector string) error {
	if err := s.store.HDel(ctx, domain.RedisDKIMPrivKeys, domain.DKIMKeyField(selector, domainName)); err != nil {
		return err
	}
	active, ok, err := s.store.HGet(ctx, domain.RedisDKIMSelectors, domainName)
	if err != nil {
		return err
	}
	if ok && active == selector {
		return s.store.HDel(ctx, domain.RedisDKIMSelectors, domainName)
	}
	return nil
}

// RemoveDKIMDomain retira todas las claves del dominio y su selector activo, y devuelve cuantas
// claves privadas habia.
func (s *RedisSync) RemoveDKIMDomain(ctx context.Context, domainName string) (int, error) {
	own, err := s.dkimFieldsOf(ctx, domainName)
	if err != nil {
		return 0, err
	}
	if err := s.store.HDel(ctx, domain.RedisDKIMPrivKeys, own...); err != nil {
		return 0, err
	}
	return len(own), s.store.HDel(ctx, domain.RedisDKIMSelectors, domainName)
}

// dkimFieldsOf lista los campos de DKIM_PRIV_KEYS del dominio. El patron glob casa tambien con
// sub.dominio: se filtra por el dominio exacto de cada campo.
func (s *RedisSync) dkimFieldsOf(ctx context.Context, domainName string) ([]string, error) {
	fields, err := s.store.HKeys(ctx, domain.RedisDKIMPrivKeys, "*."+domainName)
	if err != nil {
		return nil, err
	}
	var own []string
	for _, f := range fields {
		if _, d, ok := domain.SplitDKIMKeyField(f); ok && d == domainName {
			own = append(own, f)
		}
	}
	return own, nil
}

// PushRateLimitLog apila una linea en RL_LOG recortando la lista a maxLen.
func (s *RedisSync) PushRateLimitLog(ctx context.Context, entry domain.RateLimitLog, maxLen int64) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return s.store.LPushTrim(ctx, domain.RedisRateLimitLog, string(data), maxLen)
}

func (s *RedisSync) syncFlag(ctx context.Context, key, field string, on bool) error {
	if on {
		return s.store.HSet(ctx, key, field, "1")
	}
	return s.store.HDel(ctx, key, field)
}

// reconcileHash deja un hash exactamente con los campos deseados: escribe lo que falta
// o difiere y borra lo que sobra. No se vacia y se rellena porque los motores leen el
// hash en cualquier momento y un vacio intermedio seria un dominio desconocido.
func (s *RedisSync) reconcileHash(ctx context.Context, key string, want map[string]string) error {
	have, err := s.store.HGetAll(ctx, key)
	if err != nil {
		return err
	}
	for field, value := range want {
		if have[field] != value {
			if err := s.store.HSet(ctx, key, field, value); err != nil {
				return err
			}
		}
	}
	var stale []string
	for field := range have {
		if _, ok := want[field]; !ok {
			stale = append(stale, field)
		}
	}
	if len(stale) > 0 {
		return s.store.HDel(ctx, key, stale...)
	}
	return nil
}
