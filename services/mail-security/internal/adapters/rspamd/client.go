// Package rspamd habla con el controller de Rspamd (11334): entrena el clasificador
// (/learnspam, /learnham) y lee sus contadores e historial (/stat, /history). El controller
// exige contrasena salvo desde secure_ip (solo loopback en la configuracion copiada), asi que
// sin RSPAMD_CONTROLLER_PASSWORD no se intenta. La contrasena viaja solo en la cabecera
// Password, nunca en la URL, y ningun error la repite. No hay aqui ninguna ruta que escriba
// configuracion del controller.
package rspamd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/shopspring/decimal"
)

const (
	// maxStatBody y maxHistoryBody acotan lo que se lee del controller: /stat son unos KiB y /history
	// devuelve como mucho las nrows de history_redis.conf (1000 filas de unos KiB cada una).
	maxStatBody    = 1 << 20
	maxHistoryBody = 8 << 20
	readTimeout    = 15 * time.Second
	learnTimeout   = 30 * time.Second
)

type Client struct {
	baseURL  string
	password string
	learn    *httpclient.Client
	read     *httpclient.Client
}

// New recibe la URL del controller (por defecto http://rspamd:11334) y su contrasena.
func New(baseURL, password string) *Client {
	return &Client{
		baseURL:  baseURL,
		password: password,
		learn:    httpclient.New("rspamd-controller", httpclient.Options{Timeout: learnTimeout, MaxAttempts: 1}),
		read:     httpclient.New("rspamd-controller-read", httpclient.Options{Timeout: readTimeout, MaxAttempts: 1}),
	}
}

func (c *Client) LearnSpam(ctx context.Context, msg []byte) error {
	return c.learnWith(ctx, "/learnspam", msg)
}

// LearnHam entrena el clasificador con un mensaje legitimo (el que un dueno libero de la cuarentena).
func (c *Client) LearnHam(ctx context.Context, msg []byte) error {
	return c.learnWith(ctx, "/learnham", msg)
}

func (c *Client) learnWith(ctx context.Context, path string, msg []byte) error {
	if c.password == "" {
		return domain.ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Password", c.password)
	req.Header.Set("Content-Type", "message/rfc822")
	resp, err := c.learn.Do(req)
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
		// 208: el mensaje ya estaba aprendido con esa clase.
		return nil
	default:
		return fmt.Errorf("controller de rspamd: %d %s", resp.StatusCode, bytes.TrimSpace(body))
	}
}

// Stats lee GET /stat.
func (c *Client) Stats(ctx context.Context) (domain.RspamdStats, error) {
	body, err := c.get(ctx, "/stat", maxStatBody)
	if err != nil {
		return domain.RspamdStats{}, err
	}
	var raw statResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return domain.RspamdStats{}, fmt.Errorf("%w: /stat ilegible: %w", domain.ErrEngineCommand, err)
	}
	return raw.toDomain(), nil
}

// History lee GET /history: las filas que el controller conserva, sin orden garantizado.
func (c *Client) History(ctx context.Context) ([]domain.RspamdHistoryRow, error) {
	body, err := c.get(ctx, "/history", maxHistoryBody)
	if err != nil {
		return nil, err
	}
	var raw historyResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: /history ilegible: %w", domain.ErrEngineCommand, err)
	}
	rows := make([]domain.RspamdHistoryRow, 0, len(raw.Rows))
	for _, r := range raw.Rows {
		rows = append(rows, r.toDomain())
	}
	return rows, nil
}

// get hace una lectura autenticada y acotada. 401 y 403 son configuracion (contrasena), no un fallo
// del motor.
func (c *Client) get(ctx context.Context, path string, maxBody int64) ([]byte, error) {
	if c.password == "" {
		return nil, domain.ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Password", c.password)
	req.Header.Set("Accept", "application/json")
	resp, err := c.read.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: controller de rspamd: %w", domain.ErrEngineUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: controller de rspamd: %w", domain.ErrEngineUnreachable, err)
	}
	if int64(len(body)) > maxBody {
		return nil, fmt.Errorf("%w: la respuesta de %s supera %d bytes", domain.ErrEngineCommand, path, maxBody)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: el controller rechazo la contrasena", domain.ErrNotConfigured)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: controller de rspamd: %d", domain.ErrEngineUnreachable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: controller de rspamd: %d", domain.ErrEngineCommand, resp.StatusCode)
	}
}

// statResponse es la parte de GET /stat que se lee.
type statResponse struct {
	Version            string            `json:"version"`
	Uptime             json.Number       `json:"uptime"`
	Scanned            json.Number       `json:"scanned"`
	Learned            json.Number       `json:"learned"`
	SpamCount          json.Number       `json:"spam_count"`
	HamCount           json.Number       `json:"ham_count"`
	Actions            map[string]int64  `json:"actions"`
	Connections        json.Number       `json:"connections"`
	ControlConnections json.Number       `json:"control_connections"`
	TotalLearns        json.Number       `json:"total_learns"`
	ScanTimes          []decimal.Decimal `json:"scan_times"`
	FuzzyHashes        map[string]int64  `json:"fuzzy_hashes"`
	Statfiles          []struct {
		Symbol    string      `json:"symbol"`
		Type      string      `json:"type"`
		Revision  json.Number `json:"revision"`
		Used      json.Number `json:"used"`
		Total     json.Number `json:"total"`
		Size      json.Number `json:"size"`
		Languages json.Number `json:"languages"`
		Users     json.Number `json:"users"`
	} `json:"statfiles"`
}

func (s statResponse) toDomain() domain.RspamdStats {
	out := domain.RspamdStats{
		Version: s.Version, UptimeSeconds: integer(s.Uptime), Scanned: integer(s.Scanned), Learned: integer(s.Learned),
		SpamCount: integer(s.SpamCount), HamCount: integer(s.HamCount), Actions: s.Actions,
		Connections: integer(s.Connections), ControlConnections: integer(s.ControlConnections),
		TotalLearns: integer(s.TotalLearns), FuzzyHashes: s.FuzzyHashes,
		Statfiles: make([]domain.RspamdStatfile, 0, len(s.Statfiles)),
		ScanTime:  domain.NewRspamdScanTime(s.ScanTimes),
	}
	if out.Actions == nil {
		out.Actions = map[string]int64{}
	}
	if out.FuzzyHashes == nil {
		out.FuzzyHashes = map[string]int64{}
	}
	for _, f := range s.Statfiles {
		out.Statfiles = append(out.Statfiles, domain.RspamdStatfile{
			Symbol: f.Symbol, Type: f.Type, Revision: integer(f.Revision), Used: integer(f.Used), Total: integer(f.Total),
			Size: integer(f.Size), Languages: integer(f.Languages), Users: integer(f.Users),
		})
	}
	return out
}

// historyResponse es la parte de GET /history (history_redis, version 2) que se lee.
type historyResponse struct {
	Rows []historyRow `json:"rows"`
}

type historyRow struct {
	ID            string          `json:"id"`
	UnixTime      decimal.Decimal `json:"unix_time"`
	IP            string          `json:"ip"`
	User          string          `json:"user"`
	SenderSMTP    string          `json:"sender_smtp"`
	RcptSMTP      addressList     `json:"rcpt_smtp"`
	Subject       string          `json:"subject"`
	Score         decimal.Decimal `json:"score"`
	RequiredScore decimal.Decimal `json:"required_score"`
	Action        string          `json:"action"`
	Symbols       map[string]struct {
		Score decimal.Decimal `json:"score"`
	} `json:"symbols"`
	Size      json.Number     `json:"size"`
	ScanTime  decimal.Decimal `json:"scan_time"`
	IsSkipped bool            `json:"is_skipped"`
}

func (r historyRow) toDomain() domain.RspamdHistoryRow {
	symbols := make([]domain.RspamdSymbol, 0, len(r.Symbols))
	for name, s := range r.Symbols {
		symbols = append(symbols, domain.RspamdSymbol{Name: name, Score: s.Score})
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i].Name < symbols[j].Name })
	recipients := []string(r.RcptSMTP)
	if recipients == nil {
		recipients = []string{}
	}
	return domain.RspamdHistoryRow{
		ID: r.ID, Time: time.Unix(r.UnixTime.IntPart(), 0).UTC(), IP: r.IP, User: r.User,
		Sender: r.SenderSMTP, Recipients: recipients, Subject: domain.TruncateRspamdSubject(r.Subject),
		Score: r.Score, RequiredScore: r.RequiredScore, Action: r.Action, Symbols: symbols,
		Size: integer(r.Size), ScanTimeMs: r.ScanTime.Mul(decimal.NewFromInt(1000)).Round(2), Skipped: r.IsSkipped,
	}
}

// addressList admite lo que el controller emite segun la version: una lista de direcciones o una sola
// cadena, a veces con varias separadas por coma.
type addressList []string

func (a *addressList) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*a = nil
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*a = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	var out []string
	for _, s := range strings.Split(one, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	*a = out
	return nil
}

// integer lee un numero JSON como entero; un valor ausente o no entero vale 0 en vez de romper la
// respuesta entera.
func integer(n json.Number) int64 {
	if n == "" {
		return 0
	}
	if v, err := n.Int64(); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(string(n), 64); err == nil {
		return int64(f)
	}
	return 0
}
