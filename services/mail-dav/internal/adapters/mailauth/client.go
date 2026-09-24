// Package mailauth verifica la credencial de un buzon contra mail-auth con el contrato que usa Dovecot
// (passwd-verify.lua), con service "dav". mail-auth es la unica autoridad: aplica el freno de fuerza
// bruta por buzon e IP, el estado del buzon y el flag dav_access, y devuelve la empresa y el buzon.
package mailauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

const (
	service          = "dav"
	maxResponseBytes = 4 << 10
)

// CellResolver dice en que celda esta el dominio de un buzon.
type CellResolver interface {
	CellOf(ctx context.Context, mailDomain string) (string, error)
}

type Config struct {
	// BaseURL es el mail-auth de la celda base. Vacia CellURLs, el despliegue es de una celda y todo
	// va a BaseURL sin preguntar a nadie.
	BaseURL string
	// BaseCell es la celda que sirve BaseURL; obligatoria si hay CellURLs.
	BaseCell string
	// CellURLs son los mail-auth de las demas celdas.
	CellURLs map[string]string
	TLS      *tls.Config
	Timeout  time.Duration
}

type Client struct {
	base     string
	baseCell string
	cells    map[string]string
	resolver CellResolver
	http     *http.Client
}

// New exige HTTPS en todos los destinos: por esta llamada viaja la contrasena del buzon. resolver solo
// es necesario con varias celdas.
func New(cfg Config, resolver CellResolver) (*Client, error) {
	if len(cfg.CellURLs) > 0 && (cfg.BaseCell == "" || resolver == nil) {
		return nil, errors.New("con varias celdas hacen falta la celda base y el resolvedor de dominios")
	}
	base, err := httpsURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	cells := make(map[string]string, len(cfg.CellURLs))
	for code, raw := range cfg.CellURLs {
		if !tenantcell.ValidCode(code) || code == cfg.BaseCell {
			return nil, fmt.Errorf("celda %q no válida o repetida con la base", code)
		}
		if cells[code], err = httpsURL(raw); err != nil {
			return nil, fmt.Errorf("celda %s: %w", code, err)
		}
	}
	return &Client{
		base: base, baseCell: cfg.BaseCell, cells: cells, resolver: resolver,
		http: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				// Sin proxy de entorno: la contrasena no debe salir por un HTTPS_PROXY heredado.
				Proxy:                 nil,
				TLSClientConfig:       cfg.TLS,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: cfg.Timeout,
				MaxIdleConns:          32,
				IdleConnTimeout:       90 * time.Second,
			},
			// Una redireccion reenviaria la contrasena a otro destino.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func httpsURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("la URL de mail-auth debe ser https sin credenciales, consulta ni fragmento: %q", raw)
	}
	return u.String(), nil
}

// endpoint elige el mail-auth de la celda del buzon. Un dominio que organization no conoce va a la
// base, que responde lo mismo que a una contrasena mala: el servicio no distingue buzones de celdas.
func (c *Client) endpoint(ctx context.Context, username string) (string, error) {
	if len(c.cells) == 0 {
		return c.base, nil
	}
	_, mailDomain, _ := strings.Cut(username, "@")
	cell, err := c.resolver.CellOf(ctx, mailDomain)
	switch {
	case errors.Is(err, tenantcell.ErrUnknownDomain):
		return c.base, nil
	case err != nil:
		return "", fmt.Errorf("%w: celda del buzón: %v", domain.ErrUnavailable, err)
	case cell == c.baseCell:
		return c.base, nil
	}
	target, ok := c.cells[cell]
	if !ok {
		return "", fmt.Errorf("%w: la celda %s no tiene mail-auth declarado", domain.ErrUnavailable, cell)
	}
	return target, nil
}

type verifyRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	RealRIP  string `json:"real_rip"`
	Service  string `json:"service"`
}

type verifyResponse struct {
	Success   bool   `json:"success"`
	Username  string `json:"username"`
	TenantID  string `json:"tenant_id"`
	MailboxID string `json:"mailbox_id"`
}

// Authenticate devuelve domain.ErrInvalidCredentials para cualquier rechazo (401 o success false) y
// domain.ErrUnavailable para todo lo demas, de modo que un fallo de mail-auth no se confunda con una
// contrasena mala ni haga que el cliente descarte la suya.
func (c *Client) Authenticate(ctx context.Context, username, password, remoteIP string) (domain.Principal, error) {
	name, ok := domain.NormalizeUsername(username)
	if !ok || password == "" || remoteIP == "" {
		return domain.Principal{}, domain.ErrInvalidCredentials
	}
	target, err := c.endpoint(ctx, name)
	if err != nil {
		return domain.Principal{}, err
	}
	body, err := json.Marshal(verifyRequest{Username: name, Password: password, RealRIP: remoteIP, Service: service})
	if err != nil {
		return domain.Principal{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return domain.Principal{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Principal{}, fmt.Errorf("%w: mail-auth: %v", domain.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return domain.Principal{}, fmt.Errorf("%w: mail-auth: %v", domain.ErrUnavailable, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return principalFrom(raw, name)
	case http.StatusUnauthorized:
		return domain.Principal{}, domain.ErrInvalidCredentials
	default:
		return domain.Principal{}, fmt.Errorf("%w: mail-auth respondió %d", domain.ErrUnavailable, resp.StatusCode)
	}
}

// principalFrom exige que la respuesta sea completa y hable del buzon que se pregunto: una identidad
// que no encaje no abre ninguna base.
func principalFrom(raw []byte, asked string) (domain.Principal, error) {
	var out verifyResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return domain.Principal{}, fmt.Errorf("%w: respuesta de mail-auth ilegible", domain.ErrUnavailable)
	}
	if !out.Success {
		return domain.Principal{}, domain.ErrInvalidCredentials
	}
	tenantID, terr := uuid.Parse(out.TenantID)
	mailboxID, merr := uuid.Parse(out.MailboxID)
	if terr != nil || merr != nil || tenantID == uuid.Nil || mailboxID == uuid.Nil || out.Username != asked {
		return domain.Principal{}, fmt.Errorf("%w: mail-auth no devolvió la identidad del buzón", domain.ErrUnavailable)
	}
	return domain.Principal{TenantID: tenantID, MailboxID: mailboxID, Username: out.Username}, nil
}
