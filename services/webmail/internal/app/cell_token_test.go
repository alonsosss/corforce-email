package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// testCell es la celda de la instancia de las pruebas.
const testCell = "pe-01"

// El token de sesion lleva la celda de la instancia que lo abrio: es por lo que el gateway lleva
// cada peticion a su celda.
func TestElTokenLlevaLaCeldaDeLaInstancia(t *testing.T) {
	h := newHarness(t)
	token, _ := h.login(t)
	if cell, ok := domain.ParseSessionToken(token); !ok || cell != testCell || !strings.HasPrefix(token, testCell+".") {
		t.Fatalf("token %q: celda %q %v", token, cell, ok)
	}
}

// Un token de otra celda (o sin celda, el formato anterior) se rechaza sin buscarlo en el
// almacen, se cierre sesion con el o llegue como cookie previa a un inicio de sesion: no toca
// ninguna sesion de esta celda.
func TestUnTokenDeOtraCeldaSeRechazaSinBuscarlo(t *testing.T) {
	h := newHarness(t)
	token, _ := h.login(t)
	_, secret, _ := strings.Cut(token, ".")
	foreign := "pe-02." + secret

	h.store.failGet = errors.New("el almacen no debe consultarse")
	for _, tok := range []string{foreign, secret, "PE-01." + secret} {
		if _, err := h.svc.Authenticate(ctx, tok); err != domain.ErrSessionInvalid {
			t.Fatalf("%q: %v", tok, err)
		}
		if err := h.svc.Logout(ctx, tok); err != nil {
			t.Fatalf("cerrar sesion con %q: %v", tok, err)
		}
	}
	h.store.failGet = nil

	if _, _, err := h.svc.Login(ctx, testUser, testPass, testIP, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("la sesion de esta celda sigue abierta: %v", err)
	}
}

// Sin una celda valida el webmail no se construye: la celda va en cada token.
func TestNewExigeUnaCeldaValida(t *testing.T) {
	h := newHarness(t)
	for _, cell := range []string{"", "pe.01", "PE-01", "pe_01"} {
		_, err := New(Deps{
			Auth: h.auth, Sessions: h.store, Mail: h.mail, Sender: h.sender, Directory: h.directory, Vacations: h.directory, AddressBook: h.directory, Ledger: h.ledger,
			Composer: h.composer, Sanitizer: h.sanitizer, Scanner: h.scanner,
			PartURL: func(string, uint32, string) string { return "" }, Logger: zap.NewNop(),
			Config: Config{
				CellCode: cell, Sessions: domain.SessionPolicy{Idle: time.Minute, Max: time.Hour},
				Limits: domain.Limits{MaxRecipients: 1, MaxMessageBytes: 1}, MaxBodyPartBytes: 1, MaxAttachmentBytes: 1,
				SendTimeout: time.Minute,
			},
		})
		if err == nil {
			t.Errorf("celda %q aceptada", cell)
		}
	}
}
