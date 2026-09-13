package tenantcell

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"go.uber.org/zap"
)

// Instancias por celda de los servicios de celda.
//
// Un servicio de celda (mail-directory, mail-security) se despliega una vez por celda. Quien lo
// llama por una empresa, el gateway o un servicio del plano de empresa como domain-service, tiene
// su destino base, que sirve la celda BaseCellEnv, y una variable <SERVICIO>_CELL_HOSTS con
// "celda=host:puerto" separados por comas para las demas. Sin celda base ni instancias el
// despliegue es de una celda: todo va al destino base y no se pregunta a organization.

// BaseCellEnv nombra la celda que sirven los destinos base de los servicios de celda. La leen
// el gateway y domain-service: el despliegue se declara una sola vez para los dos caminos.
const BaseCellEnv = "GATEWAY_BASE_CELL_CODE"

// ErrNotServed: la celda de la empresa no tiene instancia declarada del servicio. Es un error de
// despliegue, no de la peticion.
var ErrNotServed = errors.New("la celda de la empresa no tiene instancia declarada del servicio")

// Instances son las instancias por celda de un conjunto de servicios de celda.
type Instances struct {
	// BaseCell es la celda de los destinos base; vacia en un despliegue de una celda.
	BaseCell string
	// ByEnv: variable <SERVICIO>_CELL_HOSTS -> celda -> URL de su instancia. Nunca incluye
	// BaseCell.
	ByEnv map[string]map[string]string
}

// LoadInstances lee con getenv la celda base (baseEnv) y las instancias de cada variable de
// envs. Una entrada mal formada, instancias sin celda base, una celda base mal formada o
// declarada tambien como instancia son error: quien las carga no debe arrancar.
func LoadInstances(getenv func(string) string, baseEnv string, envs ...string) (Instances, error) {
	sorted := append([]string(nil), envs...)
	sort.Strings(sorted)
	inst := Instances{ByEnv: make(map[string]map[string]string, len(sorted))}
	declared := false
	for _, env := range sorted {
		targets, err := ParseInstances(env, getenv(env))
		if err != nil {
			return Instances{}, err
		}
		inst.ByEnv[env] = targets
		declared = declared || len(targets) > 0
	}

	base := strings.TrimSpace(getenv(baseEnv))
	switch {
	case base == "" && declared:
		return Instances{}, fmt.Errorf("%s es obligatorio cuando %s declaran instancias: sin el no se sabe que celda sirven los destinos base", baseEnv, strings.Join(sorted, " o "))
	case base == "":
		return inst, nil
	case !ValidCode(base):
		return Instances{}, fmt.Errorf("%s: %q no es un codigo de celda", baseEnv, base)
	}
	for _, env := range sorted {
		if _, dup := inst.ByEnv[env][base]; dup {
			return Instances{}, fmt.Errorf("%s: la celda %q es la de los destinos base (%s) y no se declara tambien como instancia", env, base, baseEnv)
		}
	}
	inst.BaseCell = base
	return inst, nil
}

// ParseInstances interpreta "celda=host:puerto" separados por comas y devuelve la URL http de
// cada instancia. Vacia no declara ninguna.
func ParseInstances(envName, raw string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		code, hostport, ok := strings.Cut(strings.TrimSpace(entry), "=")
		code, hostport = strings.TrimSpace(code), strings.TrimSpace(hostport)
		if !ok || !ValidCode(code) {
			return nil, fmt.Errorf("%s: entrada %q: se espera celda=host:puerto con el codigo de la celda", envName, entry)
		}
		if _, dup := out[code]; dup {
			return nil, fmt.Errorf("%s: celda %q repetida", envName, code)
		}
		host, port, err := net.SplitHostPort(hostport)
		n, perr := strconv.Atoi(port)
		if err != nil || host == "" || strings.ContainsAny(host, "/?#@ ") || perr != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s: la celda %q necesita host:puerto, llego %q", envName, code, hostport)
		}
		out[code] = "http://" + net.JoinHostPort(host, strconv.Itoa(n))
	}
	return out, nil
}

// OrganizationURLFromEnv lee y valida ORGANIZATION_URL, la URL interna de organization.
func OrganizationURLFromEnv() (string, error) {
	return organizationURL(os.Getenv("ORGANIZATION_URL"))
}

// ResolverFromEnv pregunta a organization en ORGANIZATION_URL con el token interno. Sin URL
// valida, o sin token fuera de desarrollo o prueba, es un error.
func ResolverFromEnv(logger *zap.Logger) (*Resolver, error) {
	orgURL, err := OrganizationURLFromEnv()
	if err != nil {
		return nil, err
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		return nil, err
	}
	return NewResolver(orgURL, token, logger), nil
}

// Targets elige, por la celda de cada empresa, la instancia de un servicio de celda a la que
// llamar. Nunca adivina: sin celda resuelta o sin instancia declarada no hay destino.
type Targets struct {
	base     string
	baseCell string
	byCell   map[string]string
	resolver *Resolver
}

// Targets devuelve el selector del servicio cuyas instancias declara env, con baseURL como
// destino base. Con varias celdas necesita resolver; con una no lo usa.
func (i Instances) Targets(env, baseURL string, resolver *Resolver) (*Targets, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	byCell, loaded := i.ByEnv[env]
	switch {
	case baseURL == "":
		return nil, fmt.Errorf("%s: sin destino base", env)
	case !loaded:
		return nil, fmt.Errorf("%s: no se cargaron sus instancias", env)
	case i.BaseCell != "" && resolver == nil:
		return nil, fmt.Errorf("%s: con varias celdas hace falta resolver la celda de cada empresa", env)
	}
	return &Targets{base: baseURL, baseCell: i.BaseCell, byCell: byCell, resolver: resolver}, nil
}

// For devuelve la URL de la instancia que sirve la celda de la empresa y esa celda (vacia en
// un despliegue de una celda, donde todo va al destino base sin preguntar a nadie). Los errores
// son ErrUnknownTenant, ErrUnresolved o ErrNotServed.
func (t *Targets) For(ctx context.Context, tenantID string) (target, cell string, err error) {
	if t.baseCell == "" {
		return t.base, "", nil
	}
	cell, err = t.resolver.CellOf(ctx, tenantID)
	if err != nil {
		return "", "", err
	}
	if cell == t.baseCell {
		return t.base, cell, nil
	}
	if target, ok := t.byCell[cell]; ok {
		return target, cell, nil
	}
	return "", cell, fmt.Errorf("celda %s: %w", cell, ErrNotServed)
}

// URLs devuelve todos los destinos posibles, el base primero y despues las instancias en orden
// de celda.
func (t *Targets) URLs() []string {
	out := []string{t.base}
	for _, code := range t.Cells() {
		out = append(out, t.byCell[code])
	}
	return out
}

// Cells devuelve, ordenadas, las celdas con instancia propia (sin la base).
func (t *Targets) Cells() []string {
	codes := make([]string, 0, len(t.byCell))
	for code := range t.byCell {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// BaseCell es la celda del destino base; vacia en un despliegue de una celda.
func (t *Targets) BaseCell() string { return t.baseCell }
