package maildirectorycli

import (
	"context"
	"errors"
	"testing"
	"time"
)

type directoryDouble struct {
	reads int
	list  []string
	err   error
}

func (d *directoryDouble) PrimarySenders(context.Context, string) ([]string, error) {
	d.reads++
	return d.list, d.err
}
func (d *directoryDouble) AddPrimarySender(context.Context, string, string) error    { return nil }
func (d *directoryDouble) RemovePrimarySender(context.Context, string, string) error { return nil }

func newCacheAt(d *directoryDouble, ttl time.Duration, clock *time.Time) *CachedPrimarySenders {
	c := NewCachedPrimarySenders(d, ttl)
	c.now = func() time.Time { return *clock }
	return c
}

func TestCachedPrimarySendersReadsOncePerWindow(t *testing.T) {
	d := &directoryDouble{list: []string{"a@x.test"}}
	now := time.Unix(1000, 0)
	c := newCacheAt(d, 30*time.Second, &now)
	for i := 0; i < 5; i++ {
		got, err := c.PrimarySenders(context.Background(), "bea@acme.test")
		if err != nil || len(got) != 1 || got[0] != "a@x.test" {
			t.Fatalf("lectura %d: %v %v", i, got, err)
		}
	}
	if d.reads != 1 {
		t.Fatalf("lecturas al directorio = %d, quiero 1", d.reads)
	}
	now = now.Add(31 * time.Second)
	if _, err := c.PrimarySenders(context.Background(), "bea@acme.test"); err != nil || d.reads != 2 {
		t.Fatalf("vencida la ventana debe leer de nuevo: lecturas=%d err=%v", d.reads, err)
	}
}

func TestCachedPrimarySendersIsPerMailboxAndCopies(t *testing.T) {
	d := &directoryDouble{list: []string{"a@x.test"}}
	now := time.Unix(1000, 0)
	c := newCacheAt(d, time.Minute, &now)
	first, _ := c.PrimarySenders(context.Background(), "bea@acme.test")
	first[0] = "alterada@x.test"
	again, _ := c.PrimarySenders(context.Background(), "bea@acme.test")
	if again[0] != "a@x.test" {
		t.Fatalf("quien llama modifico la cache: %v", again)
	}
	if _, _ = c.PrimarySenders(context.Background(), "ana@acme.test"); d.reads != 2 {
		t.Fatalf("cada buzon tiene su entrada: lecturas=%d", d.reads)
	}
}

func TestCachedPrimarySendersWriteInvalidates(t *testing.T) {
	d := &directoryDouble{list: []string{"a@x.test"}}
	now := time.Unix(1000, 0)
	c := newCacheAt(d, time.Minute, &now)
	_, _ = c.PrimarySenders(context.Background(), "bea@acme.test")
	if err := c.AddPrimarySender(context.Background(), "bea@acme.test", "b@x.test"); err != nil {
		t.Fatal(err)
	}
	_, _ = c.PrimarySenders(context.Background(), "bea@acme.test")
	if d.reads != 2 {
		t.Fatalf("fijar un remitente debe invalidar la entrada: lecturas=%d", d.reads)
	}
}

func TestCachedPrimarySendersDoesNotCacheErrors(t *testing.T) {
	d := &directoryDouble{err: errors.New("caido")}
	now := time.Unix(1000, 0)
	c := newCacheAt(d, time.Minute, &now)
	for i := 0; i < 3; i++ {
		if _, err := c.PrimarySenders(context.Background(), "bea@acme.test"); err == nil {
			t.Fatal("el error debe llegar")
		}
	}
	if d.reads != 3 {
		t.Fatalf("un fallo no se cachea: lecturas=%d", d.reads)
	}
}
