package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Environment string
	Postgres    PostgresConfig
	Redis       RedisConfig
	NATS        NATSConfig
	JWT         JWTConfig
	Gateway     GatewayConfig
}

type PostgresConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	// Host/puerto del Postgres real (sin pgbouncer), para operaciones de control-plane
	// que requieren semantica de sesion (advisory locks de migraciones). Opcional.
	DirectHost string
	DirectPort int
	// CellDBName es la base de la CELDA (directorio de correo que leen los motores) para
	// los servicios que viven en ella. Vacio en los servicios del plano de control.
	CellDBName string
}

func (p PostgresConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=disable",
		p.User, p.Password, p.Host, p.Port, p.DBName,
	)
}

func (p PostgresConfig) TenantDSN(dbName string) string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=disable",
		p.User, p.Password, p.Host, p.Port, dbName,
	)
}

// CellDSN es la conexion a la base de la celda por pgbouncer. Falla si el servicio no
// declaro CELL_DB_NAME: un servicio de celda sin celda es un error de despliegue.
func (p PostgresConfig) CellDSN() (string, error) {
	if p.CellDBName == "" {
		return "", fmt.Errorf("CELL_DB_NAME is required for cell services")
	}
	return p.TenantDSN(p.CellDBName), nil
}

// TenantDirectDSN conecta al Postgres REAL, saltando pgbouncer. Lo necesita el plano
// de control (migraciones): con pool_mode=transaction los advisory locks de sesion no
// son fiables — el lock y el unlock pueden caer en conexiones backend distintas.
// Sin POSTGRES_DIRECT_HOST configurado cae al host normal (mismo comportamiento).
// sslmode=prefer: RDS exige TLS en conexiones directas (rds.force_ssl); el Postgres
// de desarrollo no ofrece TLS y prefer cae a texto plano sin fallar.
func (p PostgresConfig) TenantDirectDSN(dbName string) string {
	host, port := p.Host, p.Port
	if p.DirectHost != "" {
		host = p.DirectHost
		if p.DirectPort != 0 {
			port = p.DirectPort
		}
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=prefer",
		p.User, p.Password, host, port, dbName,
	)
}

type RedisConfig struct {
	Host     string
	Port     int
	Password string
}

func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

type NATSConfig struct {
	URL string
}

type JWTConfig struct {
	Secret     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

type GatewayConfig struct {
	Port int
}

func Load() (*Config, error) {
	cfg := &Config{
		Environment: getEnv("ENVIRONMENT", "development"),
		Postgres: PostgresConfig{
			Host:       getEnv("POSTGRES_HOST", "localhost"),
			Port:       getEnvInt("POSTGRES_PORT", 5432),
			User:       getEnv("POSTGRES_USER", "mail_admin"),
			Password:   getEnv("POSTGRES_PASSWORD", ""),
			DBName:     getEnv("POSTGRES_DB", "mail_registry"),
			DirectHost: getEnv("POSTGRES_DIRECT_HOST", ""),
			DirectPort: getEnvInt("POSTGRES_DIRECT_PORT", 0),
			CellDBName: getEnv("CELL_DB_NAME", ""),
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnvInt("REDIS_PORT", 6379),
			Password: getEnv("REDIS_PASSWORD", ""),
		},
		NATS: NATSConfig{
			URL: getEnv("NATS_URL", "nats://localhost:4222"),
		},
		JWT: JWTConfig{
			Secret: getEnv("JWT_SECRET", ""),
			// 5 min (antes 15): al no haber revocacion en caliente del access token, su
			// duracion ES la ventana en que un token robado o ya revocado sigue sirviendo.
			// El cliente renueva en memoria, asi que solo cambia la frecuencia de refresco.
			AccessTTL:  getEnvDuration("JWT_ACCESS_TTL", 5*time.Minute),
			RefreshTTL: getEnvDuration("JWT_REFRESH_TTL", 168*time.Hour),
		},
		Gateway: GatewayConfig{
			Port: getEnvInt("GATEWAY_PORT", 8080),
		},
	}

	if cfg.JWT.Secret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	if cfg.Postgres.Password == "" {
		return nil, fmt.Errorf("POSTGRES_PASSWORD is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return v
}
