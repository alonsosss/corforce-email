// Package doveadm habla con el API HTTP de doveadm de la celda (Dovecot 2.3, POST /doveadm/v1):
// la revocacion de las credenciales de un buzon (deploy/mail/README.md, "Revocacion en Dovecot").
package doveadm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

const (
	apiPath          = "/doveadm/v1"
	defaultTimeout   = 10 * time.Second
	maxResponseBytes = 64 << 10

	tagFlush = "flush"
	tagKick  = "kick"

	// exitNoUsersKicked es la salida de doveadm kick cuando no hay sesiones que cerrar
	// (DOVEADM_EX_NOTFOUND): para una revocacion es un exito, no queda nadie dentro.
	exitNoUsersKicked = 68
)

// apiKeyPattern es la forma de DOVEADM_API_KEY que admite tambien el entrypoint de Dovecot, que
// la escribe tal cual en su configuracion.
var apiKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

// Config describe el listener http de doveadm de la celda.
type Config struct {
	// BaseURL es https://<host>:<puerto>, sin ruta: nunca en claro, la clave viaja en cada peticion.
	BaseURL string
	APIKey  string
	// ServerName es el nombre que presenta el certificado de Dovecot (el de MAIL_HOSTNAME).
	ServerName string
	// CAFile suma una CA propia a las del sistema.
	CAFile  string
	Timeout time.Duration
}

// Client implementa ports.EngineSessions.
type Client struct {
	endpoint      string
	authorization string
	http          *http.Client
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, errors.New("DOVEADM_API_URL debe ser https://host:puerto, sin ruta ni credenciales")
	}
	if !apiKeyPattern.MatchString(cfg.APIKey) {
		return nil, errors.New("DOVEADM_API_KEY debe tener de 32 a 256 caracteres de [A-Za-z0-9_-]")
	}
	serverName := strings.TrimSpace(cfg.ServerName)
	if serverName == "" {
		return nil, errors.New("falta el nombre del certificado de Dovecot (DOVEADM_API_TLS_SERVER_NAME o MAIL_HOSTNAME)")
	}
	tlsCfg, err := config.ClientTLS(serverName, strings.TrimSpace(cfg.CAFile))
	if err != nil {
		return nil, fmt.Errorf("TLS del API de doveadm: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	transport := &http.Transport{
		// Sin proxy: la clave no sale de la red de la celda.
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: timeout,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		endpoint:      "https://" + u.Host + apiPath,
		authorization: "X-Dovecot-API " + base64.StdEncoding.EncodeToString([]byte(cfg.APIKey)),
		http: &http.Client{
			Timeout:       timeout,
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// ForgetCredentials vacia la cache de autenticacion del buzon y, con kick, cierra despues sus
// sesiones, en una sola peticion y en ese orden: un cliente echado que vuelve a entrar ya no
// encuentra la entrada vieja. Idempotente.
func (c *Client) ForgetCredentials(ctx context.Context, username string, kick bool) error {
	cmds := []any{[]any{"authCacheFlush", map[string][]string{"user": {username}}, tagFlush}}
	if kick {
		cmds = append(cmds, []any{"kick", map[string][]string{"mask": {username}}, tagKick})
	}
	body, err := json.Marshal(cmds)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrEngineCommand, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrEngineCommand, err)
	}
	req.Header.Set("Authorization", c.authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return fmt.Errorf("%w: certificado de Dovecot: %v", domain.ErrEngineRejected, err)
		}
		return fmt.Errorf("%w: %v", domain.ErrEngineUnreachable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: leer la respuesta: %v", domain.ErrEngineUnreachable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d (DOVEADM_API_KEY distinta en Dovecot y en mail-security)", domain.ErrEngineRejected, resp.StatusCode)
	case resp.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: HTTP %d", domain.ErrEngineUnreachable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: HTTP %d", domain.ErrEngineCommand, resp.StatusCode)
	case len(raw) > maxResponseBytes:
		return fmt.Errorf("%w: respuesta de mas de %d bytes", domain.ErrEngineCommand, maxResponseBytes)
	}
	results, err := parseResults(raw)
	if err != nil {
		return err
	}
	if err := results.check(tagFlush, false); err != nil {
		return err
	}
	if kick {
		return results.check(tagKick, true)
	}
	return nil
}

// result es la respuesta a una orden: ["doveadmResponse", [...], tag] o
// ["error", {"type": ..., "exitCode": ...}, tag].
type result struct {
	kind     string
	errType  string
	exitCode int
}

type results map[string]result

func parseResults(raw []byte) (results, error) {
	var items [][]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: respuesta que no es la del API de doveadm: %v", domain.ErrEngineCommand, err)
	}
	out := results{}
	for _, item := range items {
		var kind, tag string
		if len(item) != 3 || json.Unmarshal(item[0], &kind) != nil || json.Unmarshal(item[2], &tag) != nil {
			return nil, fmt.Errorf("%w: respuesta con una forma inesperada", domain.ErrEngineCommand)
		}
		r := result{kind: kind}
		if kind == "error" {
			var e struct {
				Type     string `json:"type"`
				ExitCode int    `json:"exitCode"`
			}
			if err := json.Unmarshal(item[1], &e); err != nil {
				return nil, fmt.Errorf("%w: error de doveadm ilegible: %v", domain.ErrEngineCommand, err)
			}
			r.errType, r.exitCode = e.Type, e.ExitCode
		}
		out[tag] = r
	}
	return out, nil
}

func (rs results) check(tag string, kick bool) error {
	r, ok := rs[tag]
	switch {
	case !ok:
		return fmt.Errorf("%w: sin respuesta para %s", domain.ErrEngineCommand, tag)
	case r.kind == "doveadmResponse":
		return nil
	case r.kind != "error":
		return fmt.Errorf("%w: %s respondio %q", domain.ErrEngineCommand, tag, r.kind)
	case r.errType == "unAuthorized" || r.errType == "unknownMethod":
		return fmt.Errorf("%w: %s: %s (doveadm_allowed_commands o version de Dovecot)", domain.ErrEngineRejected, tag, r.errType)
	case kick && r.errType == "exitCode" && r.exitCode == exitNoUsersKicked:
		return nil
	default:
		return fmt.Errorf("%w: %s: %s %d", domain.ErrEngineCommand, tag, r.errType, r.exitCode)
	}
}
