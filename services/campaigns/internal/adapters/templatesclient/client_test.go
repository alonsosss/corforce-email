package templatesclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

func TestPublishedVersion(t *testing.T) {
	tenant, template := uuid.New(), uuid.New()
	status, body := 200, `{"data":{"subject":"Hola","html":"<p>x</p>","text":"x","version":3}}`
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/templates/"+template.String()+"/render" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	v, err := c.PublishedVersion(context.Background(), tenant, template)
	if err != nil || v != 3 {
		t.Fatalf("version=%d err=%v", v, err)
	}
	if _, ok := got["version"]; ok {
		t.Fatal("sin version: templates usa la publicada")
	}
	if vars, ok := got["variables"].(map[string]any); !ok || len(vars) != 0 {
		t.Fatalf("variables vacias: %v", got)
	}

	cases := []struct {
		status int
		body   string
		want   error
	}{
		{404, `{"error":{"code":"NOT_FOUND","message":"no existe"}}`, domain.ErrTemplateNotFound},
		{409, `{"error":{"code":"CONFLICT","message":"la plantilla no tiene version publicada"}}`, domain.ErrNoPublishedVersion},
		{422, `{"error":{"code":"VALIDATION_ERROR","message":"falta la variable requerida"}}`, domain.ErrTemplateVersionRequired},
		{500, `{}`, ports.ErrUnavailable},
		{200, `{"data":{}}`, ports.ErrUnavailable},
		{200, `{"data":{"version":2,"kind":"transactional"}}`, domain.ErrTemplateNotMarketing},
	}
	for _, tc := range cases {
		status, body = tc.status, tc.body
		if _, err := c.PublishedVersion(context.Background(), tenant, template); !errors.Is(err, tc.want) {
			t.Errorf("%d %s: %v, se esperaba %v", tc.status, tc.body, err, tc.want)
		}
	}

	status, body = 200, `{"data":{"version":5,"kind":"marketing"}}`
	if v, err := c.PublishedVersion(context.Background(), tenant, template); err != nil || v != 5 {
		t.Fatalf("plantilla de marketing: version=%d err=%v", v, err)
	}
}
