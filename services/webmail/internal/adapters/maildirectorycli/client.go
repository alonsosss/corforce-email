// Package maildirectorycli pregunta a mail-directory con que direcciones puede enviar un
// buzon (GET /internal/mail-directory/sender-identities) y lee y cambia su respuesta automatica
// (GET y PUT /internal/mail-directory/vacation). Son llamadas internas con el token de gateway y
// sin empresa: el webmail no la conoce y el buzon es unico en la celda.
//
// Por que asi y no leyendo la base de la celda: el webmail no tiene credencial de base y no
// debe tenerla (ya guarda la credencial maestra de Dovecot); mail-directory es el dueno del
// esquema mail y la regla la evalua la base con mail.sender_identities, la misma funcion
// que usa el mapa smtpd_sender_login_maps de Postfix.
package maildirectorycli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	identitiesPath   = "/internal/mail-directory/sender-identities"
	vacationPath     = "/internal/mail-directory/vacation"
	maxResponseBytes = 1 << 20
	requestTimeout   = 5 * time.Second
)

// Client implementa ports.SenderDirectory y ports.VacationDirectory.
type Client struct {
	endpoint string
	vacation string
	token    string
	http     *httpclient.Client
}

func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("MAIL_DIRECTORY_URL debe ser una URL http(s) sin credenciales: %q", baseURL)
	}
	return &Client{
		endpoint: u.String() + identitiesPath,
		vacation: u.String() + vacationPath,
		token:    token,
		http:     httpclient.New("mail-directory", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 3}),
	}, nil
}

type identitiesResponse struct {
	Data struct {
		Addresses []string `json:"addresses"`
	} `json:"data"`
}

// SenderIdentities devuelve las direcciones en minusculas. Una que no pudiera ir en el
// sobre SMTP del webmail (por ejemplo internacionalizada) se descarta: el webmail no la
// ofreceria ni la admitiria como remitente.
func (c *Client) SenderIdentities(ctx context.Context, username string) ([]string, error) {
	endpoint := c.endpoint + "?" + url.Values{"username": {username}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: mail-directory: %v", domain.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: mail-directory respondio %d", domain.ErrUnavailable, resp.StatusCode)
	}
	var body identitiesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: respuesta de mail-directory ilegible", domain.ErrUnavailable)
	}
	out := make([]string, 0, len(body.Data.Addresses))
	for _, raw := range body.Data.Addresses {
		a, err := domain.NewAddress("from", "", raw)
		if err != nil {
			continue
		}
		out = append(out, strings.ToLower(a.Email))
	}
	return out, nil
}
