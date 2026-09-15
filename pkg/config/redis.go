package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Prefijos de entorno de los dos Redis. El de la plataforma guarda sesiones del webmail,
// cupos, el freno de fuerza bruta y la cache de politicas (ElastiCache en produccion); el
// de los motores es el redis-mail de una celda (deploy/mail), del que mail-security es el
// unico escritor.
const (
	PlatformRedisEnvPrefix = "REDIS"
	EngineRedisEnvPrefix   = "MAIL_REDIS"
)

// ErrRedisTLSRequired: el Redis de la plataforma en claro fuera de un entorno declarado de
// desarrollo o de prueba.
var ErrRedisTLSRequired = errors.New(
	"REDIS_TLS=true is required: the platform Redis is only reachable in plaintext when ENVIRONMENT is development or test")

// RedisTLS es el cifrado en transito hacia un Redis. La verificacion del certificado no se
// puede desactivar: sin CA propia valen las raices del sistema.
type RedisTLS struct {
	Enabled bool
	// CAFile es un PEM con CA que se suman a las del sistema (la CA interna de un Redis
	// propio). ElastiCache no la necesita.
	CAFile string
	// ServerName es el nombre que debe presentar el certificado cuando no es el del host.
	ServerName string
}

// RedisTLSFromEnv lee <prefix>_TLS, <prefix>_TLS_CA_FILE y <prefix>_TLS_SERVER_NAME. Un
// <prefix>_TLS que no es booleano, o una CA o un nombre con TLS apagado, es un error: el
// operador creeria cifrado un Redis que va en claro.
func RedisTLSFromEnv(prefix string) (RedisTLS, error) {
	t := RedisTLS{
		CAFile:     strings.TrimSpace(os.Getenv(prefix + "_TLS_CA_FILE")),
		ServerName: strings.TrimSpace(os.Getenv(prefix + "_TLS_SERVER_NAME")),
	}
	if v := strings.TrimSpace(os.Getenv(prefix + "_TLS")); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return RedisTLS{}, fmt.Errorf("%s_TLS=%q is not a boolean", prefix, v)
		}
		t.Enabled = enabled
	}
	if !t.Enabled && (t.CAFile != "" || t.ServerName != "") {
		return RedisTLS{}, fmt.Errorf("%s_TLS_CA_FILE or %s_TLS_SERVER_NAME is set while %s_TLS is off", prefix, prefix, prefix)
	}
	return t, nil
}

// ClientConfig es la configuracion TLS del cliente de go-redis (Options.TLSConfig): nil si
// el Redis va en claro. Minimo TLS 1.2 y verificacion del certificado siempre activa.
func (t RedisTLS) ClientConfig() (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}
	cfg, err := ClientTLS(t.ServerName, t.CAFile)
	if err != nil {
		return nil, fmt.Errorf("redis TLS: %w", err)
	}
	return cfg, nil
}

// LoadRedis lee el Redis de la plataforma (REDIS_*). Load la incluye; el gateway, que no
// carga la configuracion completa, la usa directamente.
func LoadRedis() (RedisConfig, error) {
	t, err := RedisTLSFromEnv(PlatformRedisEnvPrefix)
	if err != nil {
		return RedisConfig{}, err
	}
	port, err := EnvInt("REDIS_PORT", 6379, 1, MaxPort)
	if err != nil {
		return RedisConfig{}, err
	}
	return RedisConfig{
		Host:           getEnv("REDIS_HOST", "localhost"),
		Port:           port,
		Password:       getEnv("REDIS_PASSWORD", ""),
		TLS:            t,
		AllowPlaintext: DeclaredDevelopmentOrTest(),
	}, nil
}

// TLSConfig es ClientConfig con la exigencia del entorno: sin AllowPlaintext, un Redis en
// claro es ErrRedisTLSRequired y el servicio no debe arrancar.
func (r RedisConfig) TLSConfig() (*tls.Config, error) {
	if !r.TLS.Enabled && !r.AllowPlaintext {
		return nil, ErrRedisTLSRequired
	}
	return r.TLS.ClientConfig()
}
