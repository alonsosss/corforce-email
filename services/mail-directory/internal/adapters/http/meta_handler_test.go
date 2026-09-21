package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

type activeState struct {
	Value int    `json:"value"`
	Code  string `json:"code"`
}

// metaContract escribe a mano los nombres JSON que consume la interfaz: renombrar un
// campo en el servidor rompe esta prueba antes que la pantalla.
type metaContract struct {
	Mailbox struct {
		PasswordMinLength    int           `json:"password_min_length"`
		PasswordMaxLength    int           `json:"password_max_length"`
		LocalPartMaxLength   int           `json:"local_part_max_length"`
		DisplayNameMaxLength int           `json:"display_name_max_length"`
		Kinds                []string      `json:"kinds"`
		ActiveStates         []activeState `json:"active_states"`
	} `json:"mailbox"`
	Alias struct {
		ActiveStates []activeState `json:"active_states"`
	} `json:"alias"`
	Domain struct {
		NameMaxLength        int `json:"name_max_length"`
		LabelMaxLength       int `json:"label_max_length"`
		DescriptionMaxLength int `json:"description_max_length"`
	} `json:"domain"`
	Sieve struct {
		ScriptMaxBytes int      `json:"script_max_bytes"`
		FilterTypes    []string `json:"filter_types"`
	} `json:"sieve"`
	TLSPolicies []string `json:"tls_policies"`
	BCCMapTypes []string `json:"bcc_map_types"`
	Limits      struct {
		QuotaUnit string `json:"quota_unit"`
		Unlimited int    `json:"unlimited"`
	} `json:"limits"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
	Search struct {
		MaxLength int `json:"max_length"`
	} `json:"search"`
	DAV *struct {
		ServerURL string `json:"server_url"`
	} `json:"dav"`
}

func metaRequest(t *testing.T, roles ...string) *httptest.ResponseRecorder {
	t.Helper()
	return metaRequestWith(t, app.Deps{}, roles...)
}

func metaRequestWith(t *testing.T, deps app.Deps, roles ...string) *httptest.ResponseRecorder {
	t.Helper()
	// access-control inalcanzable: solo un rol del sistema pasa sin consultarlo.
	h := NewHandler(app.New(deps), authz.NewChecker("http://127.0.0.1:9", ""))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail-directory/meta", nil)
	ctx := middleware.WithIdentity(req.Context(), uuid.NewString(), uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestMetaDelDirectorioContrato(t *testing.T) {
	rec := metaRequest(t, middleware.RoleTenantAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	// Ninguna clave de primer nivel sobra ni falta.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &top); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"alias", "bcc_map_types", "domain", "limits", "mailbox", "dav", "pagination", "search", "sieve", "tls_policies"}
	for _, k := range wantKeys {
		if _, ok := top[k]; !ok {
			t.Errorf("falta la clave %q", k)
		}
	}
	if len(top) != len(wantKeys) {
		t.Errorf("claves de primer nivel: %d, quiero %d", len(top), len(wantKeys))
	}

	var got metaContract
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("el cuerpo no cumple el contrato: %v", err)
	}
	// Los valores salen de las constantes del dominio: si una cambia, la respuesta tambien.
	states := []activeState{{domain.ActiveOn, "active"}, {domain.ActiveReceiveOnly, "receive_only"}, {domain.ActiveOff, "inactive"}}
	checks := []struct {
		name      string
		got, want interface{}
	}{
		{"password_min_length", got.Mailbox.PasswordMinLength, domain.MinPasswordLength},
		{"password_max_length", got.Mailbox.PasswordMaxLength, domain.MaxPasswordLength},
		{"local_part_max_length", got.Mailbox.LocalPartMaxLength, domain.MaxLocalPartLength},
		{"display_name_max_length", got.Mailbox.DisplayNameMaxLength, domain.MaxDisplayNameLength},
		{"kinds", got.Mailbox.Kinds, domain.MailboxKinds()},
		{"mailbox.active_states", got.Mailbox.ActiveStates, states},
		{"alias.active_states", got.Alias.ActiveStates, states},
		{"name_max_length", got.Domain.NameMaxLength, domain.MaxDomainLength},
		{"label_max_length", got.Domain.LabelMaxLength, domain.MaxLabelLength},
		{"description_max_length", got.Domain.DescriptionMaxLength, domain.MaxDescriptionLength},
		{"script_max_bytes", got.Sieve.ScriptMaxBytes, domain.MaxSieveScriptBytes},
		{"filter_types", got.Sieve.FilterTypes, []string{domain.SieveTypePrefilter, domain.SieveTypePostfilter}},
		{"tls_policies", got.TLSPolicies, domain.TLSPolicies()},
		{"bcc_map_types", got.BCCMapTypes, []string{domain.BCCTypeSender, domain.BCCTypeRcpt}},
		{"quota_unit", got.Limits.QuotaUnit, domain.QuotaUnit},
		{"unlimited", got.Limits.Unlimited, domain.Unlimited},
		{"max_page_size", got.Pagination.MaxPageSize, app.DirectoryMeta().Pagination.MaxPageSize},
		{"default_page_size", got.Pagination.DefaultPageSize, app.DirectoryMeta().Pagination.DefaultPageSize},
		{"search.max_length", got.Search.MaxLength, domain.MaxSearchLength},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
	if got.DAV != nil {
		t.Errorf("sin MAIL_DAV_PUBLIC_URL no hay datos de conexion: %+v", got.DAV)
	}
	// NormalizePage acota a la pagina maxima que declara el contrato.
	if _, perPage, _ := app.NormalizePage(1, got.Pagination.MaxPageSize+1); perPage != got.Pagination.MaxPageSize {
		t.Errorf("max_page_size no es el tope real: %d", perPage)
	}
}

// Con la URL de mail-dav configurada el directorio la ofrece tal cual: la interfaz no la construye.
func TestMetaOfreceLaURLDeDAV(t *testing.T) {
	const url = "https://mail.acme.test/api/v1/dav/"
	rec := metaRequestWith(t, app.Deps{DAVServerURL: url}, middleware.RoleTenantAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data metaContract `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.DAV == nil || env.Data.DAV.ServerURL != url {
		t.Fatalf("dav = %+v, quiero %q", env.Data.DAV, url)
	}
}

// Sin un permiso que se pueda comprobar, las reglas no se sirven.
func TestMetaExigePermiso(t *testing.T) {
	if rec := metaRequest(t); rec.Code == http.StatusOK {
		t.Fatalf("sin rol del sistema ni access-control no debe servirse: %d", rec.Code)
	}
}
