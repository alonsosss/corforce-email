package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	// CellUser y CellPassword son la credencial propia de la celda: el rol de login que
	// crea ops/db/cell-service-role.sh, con CONNECT solo a la base de su celda. CellUser
	// vacio toma el nombre por convencion (CellServiceRole).
	CellUser     string
	CellPassword string
	// AllowPlatformCellCredential deja que un servicio de celda sin credencial propia abra
	// su base con la de plataforma. Solo lo activa un ENVIRONMENT declarado de desarrollo
	// o de prueba: en cualquier otro, sin credencial de celda el servicio no arranca.
	AllowPlatformCellCredential bool
}

// postgresURL compone un DSN con el usuario y la contrasena escapados: una contrasena con
// '@', '/' o '?' partiria la URL y pgx la rechazaria o, peor, la leeria mal.
func postgresURL(user, password, host string, port int, dbName, sslMode string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     "/" + dbName,
		RawQuery: "sslmode=" + sslMode,
	}
	return u.String()
}

func (p PostgresConfig) DSN() string {
	return postgresURL(p.User, p.Password, p.Host, p.Port, p.DBName, "disable")
}

func (p PostgresConfig) TenantDSN(dbName string) string {
	return postgresURL(p.User, p.Password, p.Host, p.Port, dbName, "disable")
}

// TenantDSNAt es TenantDSN contra el host de otra celda (organization.cells).
func (p PostgresConfig) TenantDSNAt(host string, port int, dbName string) string {
	return postgresURL(p.User, p.Password, host, port, dbName, "prefer")
}

// TenantDirectDSNAt es TenantDirectDSN contra otra celda: si el host es el del cluster
// por defecto se respeta POSTGRES_DIRECT_HOST; para cualquier otra celda su host es
// tambien su conexion directa (una celda remota se declara con su Postgres real).
func (p PostgresConfig) TenantDirectDSNAt(host string, port int, dbName string) string {
	if host == "" || host == p.Host {
		return p.TenantDirectDSN(dbName)
	}
	return p.TenantDSNAt(host, port, dbName)
}

// cellServiceRoleSuffix completa el nombre del rol de login de una celda a partir del de
// su base. ops/db/cell-service-role.sh aplica la misma regla.
const cellServiceRoleSuffix = "_svc"

// CellServiceRole es el rol de login de los servicios de la celda cuya base es cellDBName
// (mail_cell_pe_01 -> mail_cell_pe_01_svc).
func CellServiceRole(cellDBName string) string {
	return cellDBName + cellServiceRoleSuffix
}

// ErrCellCredentialRequired: un servicio de celda sin credencial propia en un entorno que
// no admite el respaldo de plataforma.
var ErrCellCredentialRequired = errors.New(
	"CELL_DB_PASSWORD is required: cell services only use the platform credential when ENVIRONMENT is development or test")

// CellConnection es como abre su base un servicio de celda.
type CellConnection struct {
	DSN  string
	User string
	// PlatformCredential indica que la conexion usa la credencial de plataforma como
	// respaldo de desarrollo, no la de la celda.
	PlatformCredential bool
}

// CellConnection resuelve la conexion a la base de la celda por pgbouncer, fallando
// cerrado: sin CELL_DB_NAME no hay celda, y sin credencial de celda solo se admite la de
// plataforma donde AllowPlatformCellCredential lo permite.
func (p PostgresConfig) CellConnection() (CellConnection, error) {
	if p.CellDBName == "" {
		return CellConnection{}, fmt.Errorf("CELL_DB_NAME is required for cell services")
	}
	switch {
	case p.CellPassword != "":
		user := p.CellUser
		if user == "" {
			user = CellServiceRole(p.CellDBName)
		}
		return CellConnection{
			DSN:  postgresURL(user, p.CellPassword, p.Host, p.Port, p.CellDBName, "disable"),
			User: user,
		}, nil
	case p.CellUser != "":
		return CellConnection{}, fmt.Errorf("CELL_DB_USER is set without CELL_DB_PASSWORD")
	case !p.AllowPlatformCellCredential:
		return CellConnection{}, ErrCellCredentialRequired
	case p.Password == "":
		return CellConnection{}, fmt.Errorf("no database credential for the cell: set CELL_DB_PASSWORD")
	}
	return CellConnection{
		DSN:                postgresURL(p.User, p.Password, p.Host, p.Port, p.CellDBName, "disable"),
		User:               p.User,
		PlatformCredential: true,
	}, nil
}

// CellDSN es el DSN de CellConnection.
func (p PostgresConfig) CellDSN() (string, error) {
	conn, err := p.CellConnection()
	if err != nil {
		return "", err
	}
	return conn.DSN, nil
}

// hasCellCredential indica que el proceso es un servicio de celda con credencial propia:
// no necesita la de plataforma y, en produccion, no debe recibirla.
func (p PostgresConfig) hasCellCredential() bool {
	return p.CellDBName != "" && p.CellPassword != ""
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
	return postgresURL(p.User, p.Password, host, port, dbName, "prefer")
}

type RedisConfig struct {
	Host     string
	Port     int
	Password string
	TLS      RedisTLS
	// AllowPlaintext deja conectar sin TLS. Solo lo activa un ENVIRONMENT declarado de
	// desarrollo o de prueba: en cualquier otro, TLSConfig exige REDIS_TLS=true.
	AllowPlaintext bool
}

func (r RedisConfig) Addr() string {
	return net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
}

type NATSConfig struct {
	URL string
}

// JWTConfig es la vida de los tokens. Las claves no pasan por aqui: la privada solo la lee
// identity (auth.SignerFromEnv) y las publicas quien verifica (auth.KeySetFromEnv), de modo
// que ningun otro servicio carga material de firma en su configuracion.
type JWTConfig struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// AllowEphemeralSigningKey deja que identity, sin JWT_SIGNING_KEY, firme con un par
	// generado al arrancar. Solo con ENVIRONMENT declarado development o test.
	AllowEphemeralSigningKey bool
}

type GatewayConfig struct {
	Port int
}

// developmentOrTestEnvironments son los valores de ENVIRONMENT que admiten los respaldos de
// desarrollo: que un servicio de celda use la credencial de plataforma y que identity firme
// con un par efimero. Se exige que ENVIRONMENT los diga: su valor por defecto no cuenta,
// porque un despliegue que olvida declararlo no puede quedar abierto.
var developmentOrTestEnvironments = map[string]bool{"development": true, "test": true}

func declaredDevelopmentOrTest(environment string) bool {
	return developmentOrTestEnvironments[strings.ToLower(strings.TrimSpace(environment))]
}

func Load() (*Config, error) {
	redisCfg, err := LoadRedis()
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Environment: getEnv("ENVIRONMENT", "development"),
		Postgres: PostgresConfig{
			Host:                        getEnv("POSTGRES_HOST", "localhost"),
			Port:                        getEnvInt("POSTGRES_PORT", 5432),
			User:                        getEnv("POSTGRES_USER", "mail_admin"),
			Password:                    getEnv("POSTGRES_PASSWORD", ""),
			DBName:                      getEnv("POSTGRES_DB", "mail_registry"),
			DirectHost:                  getEnv("POSTGRES_DIRECT_HOST", ""),
			DirectPort:                  getEnvInt("POSTGRES_DIRECT_PORT", 0),
			CellDBName:                  getEnv("CELL_DB_NAME", ""),
			CellUser:                    getEnv("CELL_DB_USER", ""),
			CellPassword:                getEnv("CELL_DB_PASSWORD", ""),
			AllowPlatformCellCredential: declaredDevelopmentOrTest(os.Getenv("ENVIRONMENT")),
		},
		Redis: redisCfg,
		NATS: NATSConfig{
			URL: getEnv("NATS_URL", "nats://localhost:4222"),
		},
		JWT: JWTConfig{
			// 5 min (antes 15): al no haber revocacion en caliente del access token, su
			// duracion ES la ventana en que un token robado o ya revocado sigue sirviendo.
			// El cliente renueva en memoria, asi que solo cambia la frecuencia de refresco.
			AccessTTL:                getEnvDuration("JWT_ACCESS_TTL", 5*time.Minute),
			RefreshTTL:               getEnvDuration("JWT_REFRESH_TTL", 168*time.Hour),
			AllowEphemeralSigningKey: declaredDevelopmentOrTest(os.Getenv("ENVIRONMENT")),
		},
		Gateway: GatewayConfig{
			Port: getEnvInt("GATEWAY_PORT", 8080),
		},
	}

	if cfg.Postgres.Password == "" && !cfg.Postgres.hasCellCredential() {
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
