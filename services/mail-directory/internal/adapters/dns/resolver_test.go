package dns

import (
	"context"
	"net"
	"reflect"
	"sort"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// servidorDNS responde por UDP a toda consulta MX con lo que fije el caso: los MX que se le den o un
// codigo de respuesta de error.
func servidorDNS(t *testing.T, rcode dnsmessage.RCode, mx ...string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buf[:n]) != nil || len(query.Questions) == 0 {
				continue
			}
			answer := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RCode: rcode},
				Questions: query.Questions,
			}
			for _, host := range mx {
				answer.Answers = append(answer.Answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{Name: query.Questions[0].Name, Type: dnsmessage.TypeMX, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.MXResource{Pref: 10, MX: dnsmessage.MustNewName(host)},
				})
			}
			if out, err := answer.Pack(); err == nil {
				_, _ = conn.WriteTo(out, from)
			}
		}
	}()
	return conn.LocalAddr().String()
}

func TestLookupMXDevuelveLosNombresPublicados(t *testing.T) {
	r := New(servidorDNS(t, dnsmessage.RCodeSuccess, "mx.plataforma.example.", "mx2.plataforma.example."))
	got, err := r.LookupMX(context.Background(), "acme.test")
	if err != nil {
		t.Fatal(err)
	}
	// Go baraja los MX de igual preferencia (RFC 2782): el orden no es parte del contrato.
	sort.Strings(got)
	if want := []string{"mx.plataforma.example.", "mx2.plataforma.example."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MX: %v", got)
	}
}

func TestUnDominioSinMXNoEsUnError(t *testing.T) {
	r := New(servidorDNS(t, dnsmessage.RCodeNameError))
	got, err := r.LookupMX(context.Background(), "acme.test")
	if err != nil || len(got) != 0 {
		t.Fatalf("NXDOMAIN: %v %v", got, err)
	}
}

// Un servidor que falla no dice nada del dominio: es un error, no una lista vacia que pareceria un
// MX que no casa.
func TestUnServidorQueFallaEsUnError(t *testing.T) {
	r := New(servidorDNS(t, dnsmessage.RCodeServerFailure))
	if got, err := r.LookupMX(context.Background(), "acme.test"); err == nil {
		t.Fatalf("SERVFAIL debe ser un error: %v", got)
	}
}
