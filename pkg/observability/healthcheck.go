package observability

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// healthcheckFlag es el argumento que convierte al propio binario del servicio en su
// cliente de healthcheck.
const healthcheckFlag = "--healthcheck"

// Las imagenes de los servicios son FROM scratch: no hay shell, curl ni wget con los que
// Docker pueda sondear el contenedor. La alternativa a engordar 69 imagenes con un binario
// externo es que el propio servicio sepa sondearse.
//
// La comprobacion se resuelve en init() y no en main() a proposito: cuando Docker ejecuta
// el healthcheck lanza un proceso nuevo con el mismo binario, y ese proceso NO debe abrir
// conexiones a la base ni suscribirse a NATS. init() corre antes que main, de modo que el
// sondeo termina y sale sin llegar a arrancar nada. Solo se activa con el argumento
// exacto, que ningun servicio interpreta.
//
// Uso (en el Dockerfile):
//
//	HEALTHCHECK CMD ["/lending", "--healthcheck", "8093"]
func init() {
	if len(os.Args) < 2 || os.Args[1] != healthcheckFlag {
		return
	}
	port := "8080"
	if len(os.Args) > 2 {
		port = os.Args[2]
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s%s", port, HealthPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
