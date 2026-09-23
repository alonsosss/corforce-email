// Package spamcheck puntua un correo renderizado con el Rspamd de la celda base por el API
// interno de mail-security (POST /internal/mail-security/spam-check, token interno). Arma un
// mensaje MIME como el que saldria (multipart/alternative con texto y HTML) para que la
// puntuacion sea la de un envio real, no la de un fragmento.
package spamcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/templates/internal/deliverability"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
)

const (
	checkPath = "/internal/mail-security/spam-check"
	// callTimeout: el panel en vivo espera la respuesta; sin ella la verificacion sale sin
	// puntuacion.
	callTimeout      = 10 * time.Second
	maxMessageBytes  = 2 << 20
	maxResponseBytes = 256 << 10
	maxSymbols       = 200
	// La ruta de mail-security admite 120 peticiones por minuto: el panel en vivo del editor
	// verifica con cada cambio, asi que la puntuacion de un mismo contenido se reutiliza durante
	// cacheTTL, con cacheMax entradas como mucho.
	cacheTTL = 10 * time.Minute
	cacheMax = 1024
)

type Client struct {
	baseURL string
	token   string
	from    string
	domain  string
	http    *httpclient.Client
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	spam    deliverability.Spam
	expires time.Time
}

// New: from es la direccion de la plataforma con la que se firma el mensaje de prueba
// (PLATFORM_FROM_EMAIL). Un solo intento: la verificacion no reintenta, degrada.
func New(baseURL, token, from string) (*Client, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil || addr.Name != "" {
		return nil, fmt.Errorf("spamcheck: %q no es una direccion de correo valida", from)
	}
	_, domain, _ := strings.Cut(addr.Address, "@")
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		from:    addr.Address,
		domain:  domain,
		http:    httpclient.New("mail-security-spam-check", httpclient.Options{Timeout: callTimeout, MaxAttempts: 1}),
		now:     time.Now,
		cache:   make(map[string]cacheEntry),
	}, nil
}

type checkRequest struct {
	Message string `json:"message"`
}

type checkResult struct {
	Score    *float64 `json:"score"`
	Required float64  `json:"required"`
	Action   string   `json:"action"`
	Symbols  []struct {
		Name        string  `json:"name"`
		Score       float64 `json:"score"`
		Description string  `json:"description"`
	} `json:"symbols"`
}

// Check puntua el correo. Un 429 o cualquier otro fallo es un error y la verificacion sale sin
// puntuacion; solo se guardan en cache las respuestas validas.
func (c *Client) Check(ctx context.Context, s ports.SpamSample) (deliverability.Spam, error) {
	key := cacheKey(s)
	if spam, ok := c.cached(key); ok {
		return spam, nil
	}
	spam, err := c.check(ctx, s)
	if err != nil {
		return deliverability.Spam{}, err
	}
	c.store(key, spam)
	return spam, nil
}

// cacheKey resume el contenido, no el MIME: este lleva Date y Message-ID nuevos en cada
// llamada.
func cacheKey(s ports.SpamSample) string {
	h := sha256.New()
	marketing := "0"
	if s.Marketing {
		marketing = "1"
	}
	for _, part := range []string{marketing, s.UnsubscribeURL, s.Subject, s.Text, s.HTML} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Client) cached(key string) (deliverability.Spam, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || !c.now().Before(e.expires) {
		return deliverability.Spam{}, false
	}
	return e.spam, true
}

func (c *Client) store(key string, spam deliverability.Spam) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.cache) >= cacheMax {
		var oldestKey string
		var oldest time.Time
		for k, e := range c.cache {
			if !now.Before(e.expires) {
				delete(c.cache, k)
				continue
			}
			if oldestKey == "" || e.expires.Before(oldest) {
				oldestKey, oldest = k, e.expires
			}
		}
		if len(c.cache) >= cacheMax {
			delete(c.cache, oldestKey)
		}
	}
	c.cache[key] = cacheEntry{spam: spam, expires: now.Add(cacheTTL)}
}

func (c *Client) check(ctx context.Context, s ports.SpamSample) (deliverability.Spam, error) {
	msg, err := c.message(s)
	if err != nil {
		return deliverability.Spam{}, err
	}
	if len(msg) > maxMessageBytes {
		return deliverability.Spam{}, fmt.Errorf("spamcheck: el mensaje ocupa %d bytes, mas de %d", len(msg), maxMessageBytes)
	}
	payload, err := json.Marshal(checkRequest{Message: string(msg)})
	if err != nil {
		return deliverability.Spam{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+checkPath, bytes.NewReader(payload))
	if err != nil {
		return deliverability.Spam{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return deliverability.Spam{}, fmt.Errorf("spamcheck: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return deliverability.Spam{}, fmt.Errorf("spamcheck: leer respuesta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return deliverability.Spam{}, fmt.Errorf("spamcheck: status %d", resp.StatusCode)
	}
	if len(body) > maxResponseBytes {
		return deliverability.Spam{}, errors.New("spamcheck: respuesta demasiado grande")
	}
	return parse(body)
}

// parse lee el resultado dentro del sobre {"data": ...} de pkg/response (o suelto). Descarta
// aqui, donde entran, los numeros que JSON no puede volver a escribir (NaN, Inf).
func parse(body []byte) (deliverability.Spam, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	raw := body
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 && envelope.Data[0] == '{' {
		raw = envelope.Data
	}
	var r checkResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return deliverability.Spam{}, fmt.Errorf("spamcheck: respuesta ilegible: %w", err)
	}
	if r.Score == nil || !finite(*r.Score) || !finite(r.Required) {
		return deliverability.Spam{}, errors.New("spamcheck: respuesta sin puntuacion valida")
	}
	out := deliverability.Spam{
		Available: true, Score: *r.Score, Required: r.Required, Action: strings.TrimSpace(r.Action),
		Symbols: make([]deliverability.SpamSymbol, 0, min(len(r.Symbols), maxSymbols)),
	}
	for _, s := range r.Symbols {
		if len(out.Symbols) == maxSymbols {
			break
		}
		if s.Name == "" || !finite(s.Score) {
			continue
		}
		out.Symbols = append(out.Symbols, deliverability.SpamSymbol{Name: s.Name, Score: s.Score, Description: s.Description})
	}
	return out, nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// message arma el MIME del correo de prueba. El remitente y el destinatario son la direccion de
// la plataforma: el mensaje no se envia, solo se puntua.
func (c *Client) message(s ports.SpamSample) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(s.Subject)
	headers := [][2]string{
		{"From", c.from},
		{"To", c.from},
		{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", c.now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", "<" + id + "@" + c.domain + ">"},
		{"MIME-Version", "1.0"},
	}
	if s.Marketing && s.UnsubscribeURL != "" {
		headers = append(headers,
			[2]string{"List-Unsubscribe", "<" + s.UnsubscribeURL + ">"},
			[2]string{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"})
	}
	headers = append(headers, [2]string{"Content-Type", `multipart/alternative; boundary="` + mw.Boundary() + `"`})

	var head bytes.Buffer
	for _, h := range headers {
		head.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	head.WriteString("\r\n")

	for _, part := range []struct{ contentType, body string }{
		{"text/plain; charset=utf-8", s.Text},
		{"text/html; charset=utf-8", s.HTML},
	} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.contentType},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), buf.Bytes()...), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
