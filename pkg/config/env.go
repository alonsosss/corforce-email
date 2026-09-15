package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MaxPort es el mayor puerto TCP: el techo de toda variable *_PORT.
const MaxPort = 65535

// EnvInt lee un entero del entorno dentro de [lo, hi], ambos incluidos. Ausente o en blanco
// vale def; cualquier otro valor ilegible o fuera del rango es un error que nombra la
// variable y el rango: un servicio no arranca con un limite que nadie eligio.
//
// El valor por defecto se valida en cada llamada, este o no la variable: un def fuera del
// rango es un error de programacion, y asi sale en el primer arranque de cualquier entorno
// y no el dia en que alguien quita la variable de produccion.
func EnvInt(key string, def, lo, hi int) (int, error) {
	return envInRange(key, def, lo, hi, "an integer", strconv.Atoi)
}

// EnvDuration es EnvInt para una duracion de time.ParseDuration ("90s", "15m", "168h").
func EnvDuration(key string, def, lo, hi time.Duration) (time.Duration, error) {
	return envInRange(key, def, lo, hi, "a duration", time.ParseDuration)
}

func envInRange[T int | time.Duration](key string, def, lo, hi T, kind string, parse func(string) (T, error)) (T, error) {
	if lo > hi || def < lo || def > hi {
		return 0, fmt.Errorf("%s: default %v is outside the allowed range [%v, %v]", key, def, lo, hi)
	}
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := parse(raw)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%s=%q must be %s between %v and %v", key, raw, kind, lo, hi)
	}
	return v, nil
}
