package maildirectorycli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Verificacion en dos pasos y contrasenas de aplicacion del buzon (docs/Plan_Webmail_Seguridad.md
// 3.2). El secreto TOTP solo viaja al activar: despues vive cifrado en mail-directory, que valida
// cada codigo una sola vez.
const (
	mfaPath           = "/internal/mail-directory/mfa"
	mfaActivatePath   = mfaPath + "/activate"
	mfaVerifyPath     = mfaPath + "/verify"
	mfaRecoveryPath   = mfaPath + "/recovery-codes"
	appPasswordsPath  = "/internal/mail-directory/app-passwords"
	maxRecoveryCodes  = 64
	maxAppPasswordLen = 256
)

// mfaErrors traduce los rechazos de la verificacion en dos pasos por su codigo.
var mfaErrors = internalapi.Errors{
	ByCode: map[string]error{
		"INVALID_MFA_CODE":    domain.ErrInvalidMFACode,
		"MFA_ALREADY_ENABLED": domain.ErrMFAAlreadyEnabled,
		"MFA_NOT_ENABLED":     domain.ErrMFANotEnabled,
	},
	Field: "code",
}

// appPasswordErrors: el unico choque al crear es el tope de contrasenas del buzon; un id que no es
// del buzon es 404.
var appPasswordErrors = internalapi.Errors{
	ByStatus: map[int]error{
		http.StatusConflict: domain.ErrAppPasswordLimit,
		http.StatusNotFound: domain.ErrAppPasswordNotFound,
	},
	Field: "name",
}

type mfaStatusData struct {
	Enabled           bool       `json:"enabled"`
	EnabledAt         *time.Time `json:"enabled_at"`
	RecoveryRemaining int        `json:"recovery_remaining"`
}

// MFAStatus lee el estado (GET /internal/mail-directory/mfa).
func (c *Client) MFAStatus(ctx context.Context, username string) (domain.MFAStatus, error) {
	var out mfaStatusData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: mfaPath, Query: usernameQuery(username), Out: &out, Errors: mfaErrors}); err != nil {
		return domain.MFAStatus{}, err
	}
	if out.RecoveryRemaining < 0 {
		return domain.MFAStatus{}, c.api.Unavailable("codigos de recuperacion restantes negativos")
	}
	return domain.MFAStatus{Enabled: out.Enabled, EnabledAt: out.EnabledAt, RecoveryRemaining: out.RecoveryRemaining}, nil
}

type recoveryCodesData struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (c *Client) recoveryCodes(out recoveryCodesData) ([]string, error) {
	if len(out.RecoveryCodes) == 0 || len(out.RecoveryCodes) > maxRecoveryCodes {
		return nil, c.api.Unavailable("respuesta sin codigos de recuperacion")
	}
	return out.RecoveryCodes, nil
}

// ActivateMFA guarda el secreto tras validar el codigo (POST .../mfa/activate). POST no se reintenta.
func (c *Client) ActivateMFA(ctx context.Context, username, secret, code string) ([]string, error) {
	body := struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}{Secret: secret, Code: code}
	var out recoveryCodesData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: mfaActivatePath, Query: usernameQuery(username), Body: body, Out: &out, Errors: mfaErrors}); err != nil {
		return nil, err
	}
	return c.recoveryCodes(out)
}

type codeBody struct {
	Code string `json:"code"`
}

// VerifyMFA valida un codigo TOTP o de recuperacion (POST .../mfa/verify) y lo gasta.
func (c *Client) VerifyMFA(ctx context.Context, username, code string) (domain.MFAVerification, error) {
	var out struct {
		Method            string `json:"method"`
		RecoveryRemaining int    `json:"recovery_remaining"`
	}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: mfaVerifyPath, Query: usernameQuery(username), Body: codeBody{Code: code}, Out: &out, Errors: mfaErrors}); err != nil {
		return domain.MFAVerification{}, err
	}
	if out.Method != "totp" && out.Method != "recovery" {
		return domain.MFAVerification{}, c.api.Unavailable("metodo de verificacion desconocido")
	}
	return domain.MFAVerification{Method: out.Method, RecoveryRemaining: out.RecoveryRemaining}, nil
}

// RegenerateRecoveryCodes valida el codigo y sustituye los de recuperacion (POST .../mfa/recovery-codes).
func (c *Client) RegenerateRecoveryCodes(ctx context.Context, username, code string) ([]string, error) {
	var out recoveryCodesData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: mfaRecoveryPath, Query: usernameQuery(username), Body: codeBody{Code: code}, Out: &out, Errors: mfaErrors}); err != nil {
		return nil, err
	}
	return c.recoveryCodes(out)
}

// DisableMFA valida el codigo y desactiva (DELETE .../mfa).
func (c *Client) DisableMFA(ctx context.Context, username, code string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: mfaPath, Query: usernameQuery(username), Body: codeBody{Code: code}, Errors: mfaErrors})
}

// appPasswordRow es una contrasena de aplicacion como la sirve la administracion de buzones.
type appPasswordRow struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	IMAPAccess  bool       `json:"imap_access"`
	POP3Access  bool       `json:"pop3_access"`
	SMTPAccess  bool       `json:"smtp_access"`
	SieveAccess bool       `json:"sieve_access"`
	DAVAccess   bool       `json:"dav_access"`
	Active      bool       `json:"active"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (r appPasswordRow) toDomain() domain.AppPassword {
	return domain.AppPassword{
		ID: strings.ToLower(r.ID), Name: r.Name, Active: r.Active, LastUsedAt: r.LastUsedAt, CreatedAt: r.CreatedAt,
		Access: domain.AppPasswordAccess{IMAP: r.IMAPAccess, POP3: r.POP3Access, SMTP: r.SMTPAccess, Sieve: r.SieveAccess, DAV: r.DAVAccess},
	}
}

// appPasswordList admite la lista tal cual (como la de administracion) o un objeto con la lista en
// items y el tope en max.
type appPasswordList struct {
	Items []appPasswordRow
	Max   int
}

func (l *appPasswordList) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &l.Items); err == nil {
		return nil
	}
	var obj struct {
		Items []appPasswordRow `json:"items"`
		Max   int              `json:"max"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	l.Items, l.Max = obj.Items, obj.Max
	return nil
}

// AppPasswords lista las contrasenas de aplicacion del buzon (GET .../app-passwords).
func (c *Client) AppPasswords(ctx context.Context, username string) (domain.AppPasswordList, error) {
	var out appPasswordList
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: appPasswordsPath, Query: usernameQuery(username), Out: &out, Errors: appPasswordErrors}); err != nil {
		return domain.AppPasswordList{}, err
	}
	list := domain.AppPasswordList{Items: make([]domain.AppPassword, 0, len(out.Items)), Max: max(out.Max, 0)}
	for _, row := range out.Items {
		if !domain.ValidUUID(strings.ToLower(row.ID)) {
			return domain.AppPasswordList{}, c.api.Unavailable("id de contraseña de aplicación inválido")
		}
		list.Items = append(list.Items, row.toDomain())
	}
	return list, nil
}

// createdAppPassword admite el registro con la contrasena al lado (como la administracion:
// {app_password, password}) o en el mismo objeto.
type createdAppPassword struct {
	appPasswordRow
	AppPassword *appPasswordRow `json:"app_password"`
	Password    string          `json:"password"`
}

// CreateAppPassword crea una contrasena de aplicacion (POST .../app-passwords). POST no se reintenta:
// un reintento tras un corte podria crear dos.
func (c *Client) CreateAppPassword(ctx context.Context, username string, in domain.AppPasswordInput) (domain.CreatedAppPassword, error) {
	body := struct {
		Name  string `json:"name"`
		IMAP  bool   `json:"imap"`
		POP3  bool   `json:"pop3"`
		SMTP  bool   `json:"smtp"`
		Sieve bool   `json:"sieve"`
		DAV   bool   `json:"dav"`
	}{Name: in.Name, IMAP: in.Access.IMAP, POP3: in.Access.POP3, SMTP: in.Access.SMTP, Sieve: in.Access.Sieve, DAV: in.Access.DAV}
	var out createdAppPassword
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: appPasswordsPath, Query: usernameQuery(username), Body: body, Out: &out, Errors: appPasswordErrors}); err != nil {
		return domain.CreatedAppPassword{}, err
	}
	row := out.appPasswordRow
	if out.AppPassword != nil {
		row = *out.AppPassword
	}
	if !domain.ValidUUID(strings.ToLower(row.ID)) || out.Password == "" || len(out.Password) > maxAppPasswordLen {
		return domain.CreatedAppPassword{}, c.api.Unavailable("respuesta de contraseña de aplicación incompleta")
	}
	return domain.CreatedAppPassword{AppPassword: row.toDomain(), Password: out.Password}, nil
}

// DeleteAppPassword borra una contrasena de aplicacion del buzon (DELETE .../app-passwords/{id}).
func (c *Client) DeleteAppPassword(ctx context.Context, username, id string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: appPasswordsPath + "/" + url.PathEscape(id), Query: usernameQuery(username), Errors: appPasswordErrors})
}

// filtersErrors traduce, al guardar reglas, los rechazos del reenvio externo con sus destinos; el
// resto es como cualquier otro ajuste (un 422 con su details.field).
var filtersErrors = internalapi.Errors{
	Map: func(status int, e *internalapi.APIError) error {
		switch {
		case status == http.StatusForbidden && e.Code == "REAUTH_REQUIRED":
			return &domain.ReauthRequiredError{Addresses: e.List("addresses")}
		case status == http.StatusUnprocessableEntity && e.Code == "EXTERNAL_FORWARDING_DISABLED":
			return &domain.ExternalForwardingDisabledError{Addresses: e.List("addresses")}
		}
		return nil
	},
	Field: "rules",
}
