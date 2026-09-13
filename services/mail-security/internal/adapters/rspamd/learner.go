// Package rspamd habla con el controller de Rspamd (11334) para entrenar el
// clasificador. El controller exige contrasena salvo desde secure_ip (solo loopback en
// la configuracion copiada), asi que sin RSPAMD_CONTROLLER_PASSWORD no se intenta.
package rspamd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

type Learner struct {
	baseURL  string
	password string
	client   *httpclient.Client
}

// New recibe la URL del controller (por defecto http://rspamd:11334) y su contrasena.
func New(baseURL, password string) *Learner {
	return &Learner{
		baseURL:  baseURL,
		password: password,
		client:   httpclient.New("rspamd-controller", httpclient.Options{Timeout: 30 * time.Second, MaxAttempts: 1}),
	}
}

func (l *Learner) LearnSpam(ctx context.Context, msg []byte) error {
	if l.password == "" {
		return domain.ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/learnspam", bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Password", l.password)
	req.Header.Set("Content-Type", "message/rfc822")
	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("controller de rspamd: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("%w: el controller rechazo la contrasena", domain.ErrNotConfigured)
	case http.StatusAlreadyReported:
		// 208: el mensaje ya estaba aprendido como spam.
		return nil
	default:
		return fmt.Errorf("controller de rspamd: %d %s", resp.StatusCode, bytes.TrimSpace(body))
	}
}
