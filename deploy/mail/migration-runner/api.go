package main

import (
	"bytes"
	"context"
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
)

const (
	maxResponseBytes = 512 << 10
	apiRequestLimit  = 30 * time.Second
	maxFolders       = 500
	maxFolderName    = 200

	phaseInitial = "initial"
	phaseCatchup = "catchup"

	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
	outcomeCancelled = "cancelled"
)

var idPattern = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

// APIError es la respuesta de error del servicio: el estado HTTP y el codigo del sobre. El mensaje
// del servicio no se propaga a los registros.
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mail-migration respondio %d %s", e.Status, e.Code)
}

func isLeaseLost(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == "LEASE_LOST"
}

// retryable dice si repetir la misma peticion puede servir: fallos de red, 429 y 5xx.
func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusTooManyRequests || ae.Status >= 500
	}
	return true
}

type Source struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type ClaimedJob struct {
	JobID       string `json:"job_id"`
	TenantID    string `json:"tenant_id"`
	LeaseID     string `json:"lease_id"`
	Attempt     int    `json:"attempt"`
	Source      Source `json:"source"`
	Destination struct {
		Username string `json:"username"`
	} `json:"destination"`
	LeaseSeconds int `json:"lease_seconds"`
}

type FolderProgress struct {
	Name            string `json:"name"`
	MessagesCopied  int    `json:"messages_copied"`
	MessagesSkipped int    `json:"messages_skipped"`
	MessagesFailed  int    `json:"messages_failed"`
}

type Progress struct {
	FoldersTotal    int              `json:"folders_total"`
	FoldersDone     int              `json:"folders_done"`
	MessagesTotal   int              `json:"messages_total"`
	MessagesCopied  int              `json:"messages_copied"`
	MessagesSkipped int              `json:"messages_skipped"`
	MessagesFailed  int              `json:"messages_failed"`
	BytesCopied     int64            `json:"bytes_copied"`
	Folders         []FolderProgress `json:"folders"`
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type heartbeatRequest struct {
	LeaseID  string   `json:"lease_id"`
	Phase    string   `json:"phase"`
	Progress Progress `json:"progress"`
}

type heartbeatReply struct {
	Cancel       bool `json:"cancel"`
	LeaseSeconds int  `json:"lease_seconds"`
}

type completeRequest struct {
	LeaseID  string    `json:"lease_id"`
	Outcome  string    `json:"outcome"`
	Progress Progress  `json:"progress"`
	Error    *JobError `json:"error"`
}

// API es el cliente de la API del ejecutor de mail-migration (contrato en deploy/mail/README.md).
// No sigue redirecciones ni usa proxies del entorno: la clave viaja en cada peticion y solo puede
// llegar al servicio configurado.
type API struct {
	base     *url.URL
	key      string
	runnerID string
	client   *http.Client
}

func NewAPI(base *url.URL, key, runnerID string) *API {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: apiRequestLimit,
		MaxIdleConns:          2,
		IdleConnTimeout:       60 * time.Second,
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       apiRequestLimit,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &API{base: base, key: key, runnerID: runnerID, client: client}
}

// Claim devuelve nil, nil cuando no hay trabajo (204).
func (a *API) Claim(ctx context.Context) (*ClaimedJob, error) {
	var envelope struct {
		Data ClaimedJob `json:"data"`
	}
	status, err := a.do(ctx, "/v1/claim", map[string]string{"runner_id": a.runnerID}, &envelope)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if err := envelope.Data.validateIdentity(); err != nil {
		return nil, fmt.Errorf("el trabajo reclamado no es valido: %w", err)
	}
	return &envelope.Data, nil
}

func (a *API) Heartbeat(ctx context.Context, job *ClaimedJob, phase string, progress Progress) (heartbeatReply, error) {
	var envelope struct {
		Data heartbeatReply `json:"data"`
	}
	req := heartbeatRequest{LeaseID: job.LeaseID, Phase: phase, Progress: progress.bounded()}
	if _, err := a.do(ctx, jobPath(job, "heartbeat"), req, &envelope); err != nil {
		return heartbeatReply{}, err
	}
	return envelope.Data, nil
}

func (a *API) Complete(ctx context.Context, job *ClaimedJob, outcome string, progress Progress, jobErr *JobError) error {
	req := completeRequest{LeaseID: job.LeaseID, Outcome: outcome, Progress: progress.bounded(), Error: jobErr}
	_, err := a.do(ctx, jobPath(job, "complete"), req, nil)
	return err
}

func jobPath(job *ClaimedJob, action string) string {
	return "/v1/tenants/" + url.PathEscape(job.TenantID) + "/jobs/" + url.PathEscape(job.JobID) + "/" + action
}

func (a *API) do(ctx context.Context, path string, body, out any) (int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	target := *a.base
	target.Path = a.base.Path + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mail-migration no responde: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("respuesta incompleta de mail-migration")
	}
	if len(raw) > maxResponseBytes {
		return resp.StatusCode, errors.New("respuesta de mail-migration demasiado grande")
	}
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return resp.StatusCode, &APIError{Status: resp.StatusCode, Code: sanitizeCode(envelope.Error.Code)}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, errors.New("respuesta de mail-migration ilegible")
		}
	}
	return resp.StatusCode, nil
}

func sanitizeCode(code string) string {
	if len(code) > 40 {
		return "UNKNOWN"
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return "UNKNOWN"
		}
	}
	return code
}

// validateIdentity comprueba lo minimo para poder hablar del trabajo con el servicio.
func (j *ClaimedJob) validateIdentity() error {
	if !idPattern.MatchString(j.JobID) || !idPattern.MatchString(j.TenantID) || !idPattern.MatchString(j.LeaseID) {
		return errors.New("identificadores")
	}
	if j.LeaseSeconds < 10 || j.LeaseSeconds > 3600 {
		j.LeaseSeconds = 90
	}
	return nil
}

// validate rechaza un trabajo malformado antes de ponerlo en una linea de ordenes: el destino no
// puede llevar el separador del usuario maestro ni nada que un cliente IMAP interprete.
func (j *ClaimedJob) validate() error {
	switch {
	case j.Source.Port < 1 || j.Source.Port > 65535:
		return errors.New("puerto de origen")
	case j.Source.TLS != "ssl" && j.Source.TLS != "starttls" && j.Source.TLS != "none":
		return errors.New("modo TLS de origen")
	case j.Source.Host == "" || j.Source.Username == "" || j.Source.Password == "":
		return errors.New("credenciales de origen incompletas")
	case hasControl(j.Source.Username) || strings.ContainsAny(j.Source.Password, "\r\n\x00") || hasControl(j.Source.Host):
		return errors.New("caracteres no admitidos en el origen")
	case !validMailbox(j.Destination.Username):
		return errors.New("buzon de destino")
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validMailbox(s string) bool {
	if len(s) < 3 || len(s) > 254 || strings.Count(s, "@") != 1 {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r >= 0x7f || strings.ContainsRune(`*"'\<>()[],;:%`, r) {
			return false
		}
	}
	return true
}

// bounded recorta lo que el servicio admite: 500 carpetas de 200 caracteres como mucho.
func (p Progress) bounded() Progress {
	folders := p.Folders
	if len(folders) > maxFolders {
		folders = folders[:maxFolders]
	}
	out := make([]FolderProgress, len(folders))
	for i, f := range folders {
		f.Name = truncateRunes(sanitizeName(f.Name), maxFolderName)
		out[i] = f
	}
	p.Folders = out
	return p
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func truncateRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
