package smtp

import (
	"errors"
	"io"
	"net/textproto"
	"os"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Tras el punto final de DATA, una respuesta de Postfix es definitiva; su ausencia deja el
// mensaje en duda y no puede tratarse como un fallo que se reintenta.
func TestClassifyEndOfData(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"rechazo definitivo", &textproto.Error{Code: 554, Msg: "5.7.1 Spam message rejected"}, domain.ErrMessageRejected},
		{"rechazo temporal", &textproto.Error{Code: 451, Msg: "4.3.0 try again"}, domain.ErrUnavailable},
		{"demasiado grande", &textproto.Error{Code: 552, Msg: "5.3.4 too big"}, domain.ErrMessageTooLarge},
		{"conexion cerrada sin respuesta", io.ErrUnexpectedEOF, domain.ErrDeliveryUncertain},
		{"plazo vencido", os.ErrDeadlineExceeded, domain.ErrDeliveryUncertain},
		{"respuesta ilegible", textproto.ProtocolError("short response: 2"), domain.ErrDeliveryUncertain},
	}
	for _, c := range cases {
		if got := classifyEndOfData(c.err); !errors.Is(got, c.want) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if got := classifyEndOfData(io.EOF); errors.Is(got, domain.ErrUnavailable) {
		t.Fatal("un corte tras el punto final no es un fallo reintentable")
	}
}
