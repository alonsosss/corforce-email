package domain

import "errors"

// Envio de prueba de una version de plantilla (POST /internal/transactional/test-send, lo
// usa templates). Sale marcado como prueba: los eventos llevan test=true y ni la analitica,
// ni la reputacion, ni la facturacion lo cuentan.
const (
	MaxTestRecipients = 5
	// TestSubjectPrefix va delante del asunto de toda prueba de plantilla para que quien la
	// recibe no la confunda con un envio real. Es un rotulo de la plataforma, no un dato de
	// la empresa.
	TestSubjectPrefix = "[Prueba] "
	// DefaultTestSendsPerHour acota las pruebas por empresa y hora cuando
	// TRANSACTIONAL_TEST_SENDS_PER_HOUR no lo fija: una prueba no se factura, asi que sin
	// tope seria una via de envio gratuita.
	DefaultTestSendsPerHour = 50
)

var (
	// ErrTestSendLimit: la empresa agoto las pruebas de la ultima hora.
	ErrTestSendLimit = errors.New("test send limit reached")
	// ErrLinkExpired: el enlace firmado es autentico pero ya caduco.
	ErrLinkExpired = errors.New("link expired")
)
