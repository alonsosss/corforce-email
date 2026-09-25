package maildirectorycli

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	signaturePath = "/internal/mail-directory/signature"
	filtersPath   = "/internal/mail-directory/filters"
	passwordPath  = "/internal/mail-directory/password"
)

type signatureBody struct {
	Enabled   bool   `json:"enabled"`
	HTML      string `json:"html"`
	Text      string `json:"text"`
	OnReplies bool   `json:"on_replies"`
}

type signatureData struct {
	signatureBody
	UpdatedAt *time.Time `json:"updated_at"`
	Limits    struct {
		MaxHTMLBytes int `json:"max_html_bytes"`
		MaxTextBytes int `json:"max_text_bytes"`
	} `json:"limits"`
}

func (d signatureData) toDomain() domain.Signature {
	return domain.Signature{
		Enabled: d.Enabled, HTML: d.HTML, Text: d.Text, OnReplies: d.OnReplies, UpdatedAt: d.UpdatedAt,
		Limits: domain.SignatureLimits{MaxHTMLBytes: d.Limits.MaxHTMLBytes, MaxTextBytes: d.Limits.MaxTextBytes},
	}
}

// Signature lee la firma del buzon (GET /internal/mail-directory/signature).
func (c *Client) Signature(ctx context.Context, username string) (domain.Signature, error) {
	var out signatureData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: signaturePath, Query: usernameQuery(username), Out: &out}); err != nil {
		return domain.Signature{}, err
	}
	return out.toDomain(), nil
}

// SetSignature la reemplaza con el HTML ya saneado y su texto. PUT reemplaza el estado entero.
func (c *Client) SetSignature(ctx context.Context, username string, in domain.SignatureInput) (domain.Signature, error) {
	body := signatureBody{Enabled: in.Enabled, HTML: in.HTML, Text: in.Text, OnReplies: in.OnReplies}
	var out signatureData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: signaturePath, Query: usernameQuery(username), Body: body, Out: &out, Errors: internalapi.Errors{Field: "html"}}); err != nil {
		return domain.Signature{}, err
	}
	return out.toDomain(), nil
}

type conditionJSON struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type actionJSON struct {
	Type     string `json:"type"`
	Folder   string `json:"folder,omitempty"`
	Address  string `json:"address,omitempty"`
	KeepCopy *bool  `json:"keep_copy,omitempty"`
}

type ruleJSON struct {
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Match      string          `json:"match"`
	Conditions []conditionJSON `json:"conditions"`
	Actions    []actionJSON    `json:"actions"`
	Stop       bool            `json:"stop"`
}

type forwardingJSON struct {
	Enabled   bool     `json:"enabled"`
	Addresses []string `json:"addresses"`
	KeepCopy  bool     `json:"keep_copy"`
}

type filtersBody struct {
	Rules      []ruleJSON     `json:"rules"`
	Forwarding forwardingJSON `json:"forwarding"`
}

// filtersRequest es lo que se envia al guardar: las reglas y, si el usuario acaba de confirmar su
// identidad en el webmail, reauthenticated.
type filtersRequest struct {
	filtersBody
	Reauthenticated bool `json:"reauthenticated,omitempty"`
}

type filtersData struct {
	filtersBody
	UpdatedAt *time.Time     `json:"updated_at"`
	Limits    map[string]int `json:"limits"`
}

// Filters lee las reglas y el reenvio del buzon (GET /internal/mail-directory/filters).
func (c *Client) Filters(ctx context.Context, username string) (domain.MailFilters, error) {
	var out filtersData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: filtersPath, Query: usernameQuery(username), Out: &out}); err != nil {
		return domain.MailFilters{}, err
	}
	return out.toDomain(), nil
}

// SetFilters los reemplaza. El directorio los valida y genera el Sieve; un rechazo vuelve con su
// details.field, y un reenvio externo nuevo sin reautenticar o prohibido por la empresa, con sus
// destinos (filtersErrors).
func (c *Client) SetFilters(ctx context.Context, username string, in domain.MailFiltersInput) (domain.MailFilters, error) {
	body := filtersRequest{filtersBody: toFiltersBody(in), Reauthenticated: in.Reauthenticated}
	var out filtersData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: filtersPath, Query: usernameQuery(username), Body: body, Out: &out, Errors: filtersErrors}); err != nil {
		return domain.MailFilters{}, err
	}
	return out.toDomain(), nil
}

func toFiltersBody(in domain.MailFiltersInput) filtersBody {
	body := filtersBody{
		Rules: make([]ruleJSON, len(in.Rules)),
		Forwarding: forwardingJSON{
			Enabled: in.Forwarding.Enabled, Addresses: nonNilStrings(in.Forwarding.Addresses), KeepCopy: in.Forwarding.KeepCopy,
		},
	}
	for i, r := range in.Rules {
		rule := ruleJSON{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Match: r.Match, Stop: r.Stop,
			Conditions: make([]conditionJSON, len(r.Conditions)), Actions: make([]actionJSON, len(r.Actions))}
		for j, cond := range r.Conditions {
			rule.Conditions[j] = conditionJSON{Field: cond.Field, Op: cond.Op, Value: cond.Value}
		}
		for j, a := range r.Actions {
			rule.Actions[j] = actionJSON{Type: a.Type, Folder: a.Folder, Address: a.Address, KeepCopy: a.KeepCopy}
		}
		body.Rules[i] = rule
	}
	return body
}

func (d filtersData) toDomain() domain.MailFilters {
	out := domain.MailFilters{
		Rules: make([]domain.FilterRule, len(d.Rules)),
		Forwarding: domain.Forwarding{
			Enabled: d.Forwarding.Enabled, Addresses: nonNilStrings(d.Forwarding.Addresses), KeepCopy: d.Forwarding.KeepCopy,
		},
		UpdatedAt: d.UpdatedAt,
		Limits:    d.Limits,
	}
	for i, r := range d.Rules {
		rule := domain.FilterRule{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Match: r.Match, Stop: r.Stop,
			Conditions: make([]domain.FilterCondition, len(r.Conditions)), Actions: make([]domain.FilterAction, len(r.Actions))}
		for j, cond := range r.Conditions {
			rule.Conditions[j] = domain.FilterCondition{Field: cond.Field, Op: cond.Op, Value: cond.Value}
		}
		for j, a := range r.Actions {
			rule.Actions[j] = domain.FilterAction{Type: a.Type, Folder: a.Folder, Address: a.Address, KeepCopy: a.KeepCopy}
		}
		out.Rules[i] = rule
	}
	return out
}

type passwordBody struct {
	Password string `json:"password"`
}

// SetPassword cambia la contrasena del buzon (PUT /internal/mail-directory/password) con la
// politica y el evento del directorio.
func (c *Client) SetPassword(ctx context.Context, username, password string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: passwordPath, Query: usernameQuery(username), Body: passwordBody{Password: password}, Errors: internalapi.Errors{Field: "password"}})
}

func usernameQuery(username string) url.Values { return url.Values{"username": {username}} }

func nonNilStrings(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
