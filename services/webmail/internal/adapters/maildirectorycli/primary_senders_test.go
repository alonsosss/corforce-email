package maildirectorycli

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestRemitentesFijadosContraElDirectorio(t *testing.T) {
	c, calls := fakeDirectory(t, map[string]reply{
		"GET " + primarySendersPath:    {http.StatusOK, `{"data":{"items":[{"address":"a@x.pe","created_at":"2030-01-01T00:00:00Z"},{"address":"b@x.pe","created_at":"2030-01-01T00:00:00Z"}],"max_items":200}}`},
		"POST " + primarySendersPath:   {http.StatusOK, `{"data":{"address":"a@x.pe","created_at":"2030-01-01T00:00:00Z"}}`},
		"DELETE " + primarySendersPath: {http.StatusNoContent, ``},
	})
	ctx := context.Background()

	list, err := c.PrimarySenders(ctx, "ana@acme.pe")
	if err != nil || len(list) != 2 || list[0] != "a@x.pe" || (*calls)[0].query != "username=ana%40acme.pe" {
		t.Fatalf("listar: %v %v %s", list, err, (*calls)[0].query)
	}
	if err := c.AddPrimarySender(ctx, "ana@acme.pe", "a@x.pe"); err != nil || (*calls)[1].body["address"] != "a@x.pe" {
		t.Fatalf("fijar: %v %+v", err, (*calls)[1].body)
	}
	if err := c.RemovePrimarySender(ctx, "ana@acme.pe", "a@x.pe"); err != nil || (*calls)[2].query != "address=a%40x.pe&username=ana%40acme.pe" {
		t.Fatalf("quitar: %v %s", err, (*calls)[2].query)
	}
}

func TestRemitentesFijadosTraducenElTopeYLaValidacion(t *testing.T) {
	c, _ := fakeDirectory(t, map[string]reply{
		"POST " + primarySendersPath: {http.StatusConflict, `{"error":{"code":"PRIMARY_SENDER_LIMIT","message":"tope"}}`},
	})
	if err := c.AddPrimarySender(context.Background(), "ana@acme.pe", "a@x.pe"); !errors.Is(err, domain.ErrPrimarySenderLimit) {
		t.Fatalf("tope: %v", err)
	}
}
