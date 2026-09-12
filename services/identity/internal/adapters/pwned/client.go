// Package pwned consulta si una contrasena aparece en filtraciones publicas, con el
// protocolo de k-anonimato de Have I Been Pwned: al servicio solo viajan los cinco
// primeros caracteres del SHA-1, y la comparacion con el resto se hace aqui. Ni la
// contrasena ni un hash que la identifique salen del servicio.
//
// Una contrasena que cumple la politica (8 caracteres, mayuscula, digito, simbolo) y
// aparece en una filtracion -"Password1!", "Admin123$"- es la primera que prueba cualquier
// ataque por diccionario. La politica mide forma; esto mide si ya esta en la lista.
package pwned

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.pwnedpasswords.com/range"
	// La respuesta con relleno (Add-Padding) ronda los 30-50 KB; el tope evita que un
	// servicio caido o suplantado nos haga leer sin fin.
	maxResponseBytes = 4 << 20
)

type Client struct {
	base    string
	http    *http.Client
	enabled bool
}

// NewFromEnv lee la configuracion del entorno. PASSWORD_BREACH_CHECK=off lo apaga (por
// ejemplo en un entorno sin salida a internet); PASSWORD_BREACH_API_URL permite apuntar a
// un espejo. Sin variables: encendido contra el servicio publico.
func NewFromEnv() *Client {
	enabled := !strings.EqualFold(strings.TrimSpace(os.Getenv("PASSWORD_BREACH_CHECK")), "off")
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("PASSWORD_BREACH_API_URL")), "/")
	if base == "" {
		base = defaultBaseURL
	}
	return New(base, enabled)
}

func New(baseURL string, enabled bool) *Client {
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		enabled: enabled,
		// Corto a proposito: la comprobacion esta en el camino de crear un usuario o
		// cambiar la contrasena, y si el servicio externo no responde se admite la
		// contrasena (lo decide el caso de uso), no se deja al usuario esperando.
		http: &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c != nil && c.enabled }

// IsBreached devuelve true si la contrasena aparece al menos una vez en las
// filtraciones conocidas. Un error significa que no se pudo comprobar, no que sea segura.
func (c *Client) IsBreached(ctx context.Context, password string) (bool, error) {
	if !c.Enabled() {
		return false, nil
	}
	sum := sha1.Sum([]byte(password))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := digest[:5], digest[5:]

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+prefix, nil)
	if err != nil {
		return false, fmt.Errorf("pwned: preparar consulta: %w", err)
	}
	// Con relleno, todas las respuestas traen un numero parecido de lineas: quien mire el
	// trafico no puede inferir el prefijo por el tamano. Las lineas de relleno vienen con
	// recuento 0 y se descartan abajo.
	req.Header.Set("Add-Padding", "true")
	req.Header.Set("User-Agent", "core-force-mail-identity")

	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("pwned: consultar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("pwned: HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxResponseBytes))
	for sc.Scan() {
		rest, count, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok || !strings.EqualFold(rest, suffix) {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSpace(count))
		return n > 0, nil
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("pwned: leer respuesta: %w", err)
	}
	return false, nil
}
