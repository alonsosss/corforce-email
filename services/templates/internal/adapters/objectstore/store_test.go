package objectstore

import (
	"testing"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
)

func TestPublicURLCuelgaDeLaBasePublica(t *testing.T) {
	objects, err := objectstore.New(objectstore.Config{Endpoint: "minio:9000", Bucket: "media", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(objects, " https://app.acme.pe/ ")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.PublicURL("public/t/templates/ab.png"); got != "https://app.acme.pe/media/public/t/templates/ab.png" {
		t.Fatalf("url: %s", got)
	}
	for _, base := range []string{"", "app.acme.pe", "ftp://app.acme.pe", "https://app.acme.pe/?x=1"} {
		if _, err := New(objects, base); err == nil {
			t.Errorf("%q deberia rechazarse", base)
		}
	}
}
