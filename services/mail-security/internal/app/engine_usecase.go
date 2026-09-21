package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Primera linea del mapa de hosts de reenvio para Rspamd: el mapa nunca puede ir vacio.
const forwardingHostsSentinel = "240.240.240.240"

// EngineUseCase atiende a Rspamd y Postfix. Estos handlers leen sin empresa en el
// contexto: el correo entra por los motores sin ninguna sesion, y un destinatario solo
// se puede atribuir a una empresa DESPUES de resolverlo en el directorio. Por eso las
// consultas corren como duena del pool (sin TransactRLS) y filtran de forma explicita
// por lo que el motor pide (direccion, dominio, IP), nunca a lo ancho.
type EngineUseCase struct {
	expander   *Expander
	tx         ports.OwnerTransactor
	documents  ports.DocumentStamps
	dir        ports.DirectoryReader
	policy     ports.PolicyReader
	quarantine ports.QuarantineRepository
	sync       *RedisSync
	store      ports.EngineStore
	events     ports.EventPublisher
	metrics    ports.QuarantineMetrics
	logger     *zap.Logger
	logLines   int64
	now        func() time.Time
}

type EngineDeps struct {
	Tx         ports.OwnerTransactor
	Documents  ports.DocumentStamps
	Directory  ports.DirectoryReader
	Policy     ports.PolicyReader
	Quarantine ports.QuarantineRepository
	Sync       *RedisSync
	Store      ports.EngineStore
	Events     ports.EventPublisher
	// Metrics es opcional: sin ellas no se cuenta nada.
	Metrics  ports.QuarantineMetrics
	Logger   *zap.Logger
	LogLines int64
}

func NewEngineUseCase(d EngineDeps) *EngineUseCase {
	return &EngineUseCase{
		expander:   NewExpander(d.Directory),
		tx:         d.Tx,
		documents:  d.Documents,
		dir:        d.Directory,
		policy:     d.Policy,
		quarantine: d.Quarantine,
		sync:       d.Sync,
		store:      d.Store,
		events:     d.Events,
		metrics:    quarantineMetricsOrNoop(d.Metrics),
		logger:     d.Logger,
		logLines:   d.LogLines,
		now:        time.Now,
	}
}

// AliasExpand resuelve /aliasexp: el username final si es exactamente uno.
func (uc *EngineUseCase) AliasExpand(ctx context.Context, rcpt string) (string, error) {
	rcpt = domain.NormalizeAddress(rcpt)
	local, _, ok := domain.SplitAddress(rcpt)
	if !ok || strings.HasPrefix(local, "postmaster") {
		return "", nil
	}
	return uc.expander.ExpandToSingle(ctx, rcpt)
}

// BCC resuelve /bcc: destino de copia para un destinatario (kind rcpt) o remitente
// (kind sender). El valor puede ser una direccion o '@dominio', tal como lo manda Rspamd.
func (uc *EngineUseCase) BCC(ctx context.Context, kind, address string) (string, bool, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	if !strings.HasPrefix(address, "@") {
		address = domain.NormalizeAddress(address)
	}
	if address == "" {
		return "", false, nil
	}
	return uc.dir.BCCDestination(ctx, kind, address)
}

// Footer resuelve /footer para el dominio del remitente autenticado.
func (uc *EngineUseCase) Footer(ctx context.Context, domainName, username, from string) (domain.FooterResponse, error) {
	empty := domain.FooterResponse{Vars: "{}"}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	username = strings.ToLower(strings.TrimSpace(username))
	from = strings.ToLower(strings.TrimSpace(from))
	if domainName == "" {
		return empty, nil
	}
	f, err := uc.policy.FooterByDomain(ctx, domainName)
	if err == domain.ErrNotFound {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	for _, excluded := range f.MailboxExclude {
		if excluded == username {
			return empty, nil
		}
	}
	if _, fromDomain, ok := domain.SplitAddress(from); ok {
		for _, excluded := range f.AliasDomainExclude {
			if excluded == fromDomain {
				return empty, nil
			}
		}
	}
	vars, err := json.Marshal(map[string]string{"from": from, "domain": domainName})
	if err != nil {
		return empty, err
	}
	resp := domain.FooterResponse{HTML: f.HTML, Plain: f.Plain, Vars: string(vars)}
	if f.SkipReplies {
		resp.SkipReplies = 1
	}
	return resp, nil
}

// ForwardingHostPermits resuelve /forwardinghosts?host=IP para postscreen.
func (uc *EngineUseCase) ForwardingHostPermits(ctx context.Context, host string) (bool, error) {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false, nil
	}
	hosts, err := uc.policy.AllForwardingHosts(ctx)
	if err != nil {
		return false, err
	}
	for _, h := range hosts {
		_, cidr, err := net.ParseCIDR(h.Host)
		if err == nil && cidr.Contains(ip) {
			return true, nil
		}
	}
	return false, nil
}

// ForwardingHostList resuelve /forwardinghosts sin host: el mapa de CIDR para Rspamd.
func (uc *EngineUseCase) ForwardingHostList(ctx context.Context) ([]string, error) {
	hosts, err := uc.policy.AllForwardingHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(hosts)+1)
	out = append(out, forwardingHostsSentinel)
	for _, h := range hosts {
		out = append(out, h.Host)
	}
	return out, nil
}

// SettingsDocument es el resultado de /settings con su marca de modificacion.
type SettingsDocument struct {
	Body         string
	LastModified time.Time
}

// Settings genera el UCL y decide si cambio desde ifModifiedSince (notModified=true
// significa responder 304). La marca de tiempo avanza cuando cambia el CONTENIDO (por su
// hash, no por updated_at): un borrado o un cambio de dominios alias en el directorio
// tambien fuerza la recarga. Vive en la base y no en memoria, asi que todas las replicas
// responden con la misma marca y el servicio puede correr con varias.
func (uc *EngineUseCase) Settings(ctx context.Context, ifModifiedSince time.Time) (SettingsDocument, bool, error) {
	in, err := uc.buildSettingsInput(ctx)
	if err != nil {
		return SettingsDocument{}, false, err
	}
	body := domain.RenderSettings(in)
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])

	stamp, err := uc.settingsStamp(ctx, hash)
	if err != nil {
		return SettingsDocument{}, false, fmt.Errorf("marca de settings: %w", err)
	}
	doc := SettingsDocument{Body: body, LastModified: stamp.LastModified}
	if stamp.ContentHash != hash {
		// Otra replica dio de alta a la vez un contenido distinto y quedo su marca. Este
		// contenido sale con una marca anterior a la guardada: el siguiente sondeo no puede
		// recibir 304 y recoge el vigente.
		doc.LastModified = stamp.LastModified.Add(-time.Second)
		return doc, false, nil
	}
	notModified := !ifModifiedSince.IsZero() && !stamp.LastModified.After(ifModifiedSince)
	return doc, notModified, nil
}

// settingsStamp devuelve la marca compartida del documento con ese hash y la adelanta si
// el contenido cambio. La fila queda bloqueada durante la decision: dos replicas que ven el
// mismo cambio no dan dos marcas distintas.
func (uc *EngineUseCase) settingsStamp(ctx context.Context, hash string) (domain.DocumentStamp, error) {
	var stamp domain.DocumentStamp
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		prev, err := uc.documents.LockDocument(ctx, domain.DocumentRspamdSettings)
		if err != nil {
			return err
		}
		var seed time.Time
		if prev == nil {
			if seed, err = uc.policy.PolicyUpdatedAt(ctx); err != nil {
				return err
			}
		}
		next, changed := domain.NextStamp(prev, domain.DocumentRspamdSettings, hash, uc.now(), seed)
		if !changed {
			stamp = next
			return nil
		}
		stamp, err = uc.documents.SaveDocument(ctx, next)
		return err
	})
	return stamp, err
}

func (uc *EngineUseCase) buildSettingsInput(ctx context.Context) (domain.SettingsInput, error) {
	var in domain.SettingsInput
	scores, err := uc.policy.AllSpamScores(ctx)
	if err != nil {
		return in, fmt.Errorf("umbrales: %w", err)
	}
	for _, s := range scores {
		rcpts, err := uc.recipientsFor(ctx, s.Object)
		if err != nil {
			return in, err
		}
		in.Scores = append(in.Scores, domain.ScoreRule{
			Object: s.Object, Kind: domain.ObjectKindOf(s.Object), Recipients: rcpts,
			HighScore: s.HighScore, LowScore: s.LowScore,
		})
	}

	lists, err := uc.policy.AllAddressLists(ctx)
	if err != nil {
		return in, fmt.Errorf("listas: %w", err)
	}
	grouped := map[string]*domain.ListRule{}
	var order []string
	for _, e := range lists {
		key := e.Object + "|" + string(e.Kind)
		rule, ok := grouped[key]
		if !ok {
			rcpts, err := uc.recipientsFor(ctx, e.Object)
			if err != nil {
				return in, err
			}
			rule = &domain.ListRule{Object: e.Object, Kind: domain.ObjectKindOf(e.Object), ListKind: e.Kind, Recipients: rcpts}
			grouped[key] = rule
			order = append(order, key)
		}
		rule.Patterns = append(rule.Patterns, e.Pattern)
	}
	for _, key := range order {
		in.Lists = append(in.Lists, *grouped[key])
	}

	maps, err := uc.policy.AllActiveSettingsMaps(ctx)
	if err != nil {
		return in, fmt.Errorf("bloques adicionales: %w", err)
	}
	for _, m := range maps {
		in.Maps = append(in.Maps, m.Content)
	}

	internal, err := uc.dir.InternalAliases(ctx)
	if err != nil {
		return in, fmt.Errorf("aliases internos: %w", err)
	}
	for _, a := range internal {
		aliasDomains, err := uc.dir.AliasDomainsOf(ctx, a.Domain)
		if err != nil {
			return in, err
		}
		in.InternalAliases = append(in.InternalAliases, domain.InternalAliasRule{
			Address: a.Address, Domains: append([]string{a.Domain}, aliasDomains...),
		})
	}
	return in, nil
}

// recipientsFor calcula las direcciones que Rspamd ve para un objeto: un dominio y sus
// dominios alias; un buzon, sus variantes en dominios alias y los aliases que llegan a
// el. Rspamd evalua el sobre ANTES de que Postfix reescriba alias, por eso hace falta.
func (uc *EngineUseCase) recipientsFor(ctx context.Context, object string) ([]string, error) {
	if domain.ObjectKindOf(object) == domain.ObjectDomain {
		return uc.dir.AliasDomainsOf(ctx, object)
	}
	local, dom, ok := domain.SplitAddress(object)
	if !ok {
		return nil, nil
	}
	aliasDomains, err := uc.dir.AliasDomainsOf(ctx, dom)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(aliasDomains))
	for _, ad := range aliasDomains {
		out = append(out, local+"@"+ad)
	}
	aliases, err := uc.dir.AliasesTargeting(ctx, object)
	if err != nil {
		return nil, err
	}
	for _, a := range aliases {
		if strings.HasPrefix(a, "@") {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out, nil
}

// PipeOutcome resume que paso con /pipe para que el handler elija el codigo HTTP.
type PipeOutcome struct {
	Stored        int
	SkippedSize   int
	SkippedDomain int
	NoMailbox     int
}

// Pipe guarda en cuarentena el mensaje que exporta Rspamd: una fila por buzon final,
// con los limites de la empresa de ese buzon.
func (uc *EngineUseCase) Pipe(ctx context.Context, meta domain.QuarantineMetadata, msg []byte) (PipeOutcome, error) {
	var out PipeOutcome
	seen := map[string]struct{}{}
	for _, rcpt := range meta.Rcpt {
		boxes, err := uc.expander.Expand(ctx, rcpt)
		if err != nil {
			return out, fmt.Errorf("resolver %s: %w", rcpt, errResolve(err))
		}
		if len(boxes) == 0 {
			out.NoMailbox++
		}
		for _, mb := range boxes {
			if _, dup := seen[mb.Username]; dup {
				continue
			}
			seen[mb.Username] = struct{}{}
			settings, err := uc.quarantineSettings(ctx, mb.TenantID)
			if err != nil {
				return out, fmt.Errorf("ajustes de %s: %w", mb.Username, errResolve(err))
			}
			if settings.ExcludesDomain(mb.Domain) {
				out.SkippedDomain++
				continue
			}
			if int64(len(msg)) > settings.MaxSizeBytes {
				out.SkippedSize++
				continue
			}
			item := domain.QuarantineItem{
				ID: uuid.New(), TenantID: mb.TenantID, QID: meta.QID, Subject: meta.Subject,
				Score: meta.Score, IP: meta.IP, Action: meta.Action, Symbols: nonNil(meta.Symbols),
				FuzzyHashes: nonNil(meta.Fuzzy), Sender: meta.From, Rcpt: mb.Username, Domain: mb.Domain,
				UserName: meta.User, Msg: msg, CreatedAt: uc.now(),
			}
			item.QHash = quarantineHash(item.ID, meta.QID)
			// La fila y su evento se confirman juntos: una cuarentena sin evento es un aviso
			// que nunca sale, y un evento sin fila, un aviso de un mensaje que no existe.
			err = uc.tx.Transact(ctx, func(ctx context.Context) error {
				if err := uc.quarantine.Insert(ctx, &item); err != nil {
					return err
				}
				return uc.events.QuarantineStored(ctx, &item)
			})
			if err != nil {
				return out, fmt.Errorf("guardar cuarentena de %s: %w", mb.Username, errStore(err))
			}
			out.Stored++
			uc.metrics.QuarantineStored()
			// La poda va fuera: un fallo aqui no debe deshacer la fila recien guardada.
			if _, err := uc.quarantine.PruneRcpt(ctx, mb.TenantID, mb.Username, settings.RetentionSize); err != nil {
				uc.logger.Warn("poda de cuarentena por buzon", zap.String("rcpt", mb.Username), zap.Error(err))
			}
		}
	}
	return out, nil
}

// PipeRateLimit apila en RL_LOG el aviso de un envio limitado.
func (uc *EngineUseCase) PipeRateLimit(ctx context.Context, entry domain.RateLimitLog) error {
	if err := uc.store.Ping(ctx); err != nil {
		return domain.ErrRedisUnavailable
	}
	if entry.Time == 0 {
		entry.Time = uc.now().Unix()
	}
	entry.RLName, entry.RLHash = domain.ParseRateLimitInfo(entry.RLInfo)
	return uc.sync.PushRateLimitLog(ctx, entry, uc.logLines)
}

func (uc *EngineUseCase) quarantineSettings(ctx context.Context, tenantID uuid.UUID) (domain.QuarantineSettings, error) {
	s, err := uc.policy.QuarantineSettingsFor(ctx, tenantID)
	if err == domain.ErrNotFound {
		return domain.DefaultQuarantineSettings(tenantID), nil
	}
	if err != nil {
		return domain.QuarantineSettings{}, err
	}
	return *s, nil
}

// quarantineHash identifica la fila en enlaces sin sesion: sha256(id || qid).
func quarantineHash(id uuid.UUID, qid string) string {
	sum := sha256.Sum256([]byte(id.String() + qid))
	return hex.EncodeToString(sum[:])
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Los errores de /pipe se clasifican para que el handler responda 502 (resolucion) o
// 503 (almacenamiento), como documenta el contrato de los motores.
type ResolveError struct{ Err error }

func (e *ResolveError) Error() string { return e.Err.Error() }
func (e *ResolveError) Unwrap() error { return e.Err }

type StoreError struct{ Err error }

func (e *StoreError) Error() string { return e.Err.Error() }
func (e *StoreError) Unwrap() error { return e.Err }

func errResolve(err error) error { return &ResolveError{Err: err} }
func errStore(err error) error   { return &StoreError{Err: err} }
