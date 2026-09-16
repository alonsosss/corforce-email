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
	Postgres PostgresConfig
	Redis    RedisConfig
	NATS     NATSConfig
	JWT      JWTConfig
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
	// RegistryUser y RegistryPassword son la credencial con la que un servicio del plano de
	// EMPRESA abre el registro: el rol de enrutado (mail_router), que solo puede leer
	// organization.v_tenant_routing. Vacias, el registro se abre con la de plataforma.
	RegistryUser     string
	RegistryPassword string
	// TenantUser y TenantPassword son la credencial propia del servicio para las bases de
	// EMPRESA: el rol mail_svc_<esquema>, con DML solo sobre su esquema. Vacias, las bases
	// de empresa se abren con la de plataforma, que es duena de las tablas de todos.
	//
	// Las migraciones NO pasan por aqui: corren como dueno (TenantDirectDSN).
	TenantUser     string
	TenantPassword string
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

// DefaultRoutingRole es el rol de login del enrutado por empresa cuando REGISTRY_DB_USER no
// lo dice. Lo crea ops/db/tenant-service-role.sh --router.
const DefaultRoutingRole = "mail_router"

// registryCredential: con que se abre la base de REGISTRO. El rol de enrutado si el
// despliegue se lo dio; si no, la de plataforma.
func (p PostgresConfig) registryCredential() (string, string) {
	if p.RegistryPassword == "" {
		return p.User, p.Password
	}
	user := p.RegistryUser
	if user == "" {
		user = DefaultRoutingRole
	}
	return user, p.RegistryPassword
}

// tenantCredential: con que se abre la base de una EMPRESA. La propia del servicio si el
// despliegue se la dio; si no, la de plataforma.
func (p PostgresConfig) tenantCredential() (string, string) {
	if p.TenantPassword == "" || p.TenantUser == "" {
		return p.User, p.Password
	}
	return p.TenantUser, p.TenantPassword
}

// HasServiceCredential indica que el proceso abre el registro y las bases de empresa con
// credenciales propias, y no con la de plataforma.
func (p PostgresConfig) HasServiceCredential() bool {
	return p.RegistryPassword != "" && p.TenantPassword != "" && p.TenantUser != ""
}

func (p PostgresConfig) DSN() string {
	user, password := p.registryCredential()
	return postgresURL(user, password, p.Host, p.Port, p.DBName, "disable")
}

func (p PostgresConfig) TenantDSN(dbName string) string {
	user, password := p.tenantCredential()
	return postgresURL(user, password, p.Host, p.Port, dbName, "disable")
}

// TenantDSNAt es TenantDSN contra el host de otra celda (organization.cells).
func (p PostgresConfig) TenantDSNAt(host string, port int, dbName string) string {
	user, password := p.tenantCredential()
	return postgresURL(user, password, host, port, dbName, "prefer")
}

// TenantDirectDSNAt es TenantDirectDSN contra otra celda: si el host es el del cluster
// por defecto se respeta POSTGRES_DIRECT_HOST; para cualquier otra celda su host es
// tambien su conexion directa (una celda remota se declara con su Postgres real).
func (p PostgresConfig) TenantDirectDSNAt(host string, port int, dbName string) string {
	if host == "" || host == p.Host {
		return p.TenantDirectDSN(dbName)
	}
	// Con la credencial de PLATAFORMA, no con la del servicio: por aqui pasan las
	// migraciones, que corren como dueno de las tablas. Un rol de servicio no hace DDL.
	return postgresURL(p.User, p.Password, host, port, dbName, "prefer")
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

// Vida de los tokens (JWT_ACCESS_TTL, JWT_REFRESH_TTL).
const (
	// Al no haber revocacion en caliente del access token mas alla de tokens_valid_from, su
	// vida ES la ventana en que un token robado o ya revocado sigue sirviendo, y la sesion
	// esta documentada con 5 minutos como mucho: se puede acortar, nunca alargar. El minimo
	// deja al menos un minuto de uso a cada token, porque el cliente web lo renueva 60 s
	// antes de vencer (web/src/api/client.ts).
	defaultAccessTTL = 5 * time.Minute
	minAccessTTL     = 2 * time.Minute
	maxAccessTTL     = 5 * time.Minute
	// El refresh admite el mismo rango que la politica de sesion de una empresa en identity
	// (refresh_ttl_hours de 1 a 8760).
	defaultRefreshTTL = 168 * time.Hour
	minRefreshTTL     = time.Hour
	maxRefreshTTL     = 8760 * time.Hour
)

// developmentOrTestEnvironments son los valores de ENVIRONMENT que admiten los respaldos de
// desarrollo (docs/Operacion_Despliegue.md, 1).
var developmentOrTestEnvironments = map[string]bool{"development": true, "test": true}

// DeclaredDevelopmentOrTest es la unica regla que relaja un control segun el entorno: cierto
// solo si ENVIRONMENT declara development o test, sin distinguir mayusculas ni espacios en
// los extremos. Sin declarar, production, staging o cualquier otro valor es estricto: un
// despliegue que olvida o escribe mal su entorno no puede quedar abierto.
func DeclaredDevelopmentOrTest() bool {
	return developmentOrTestEnvironments[strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))]
}

func Load() (*Config, error) {
	redisCfg, err := LoadRedis()
	if err != nil {
		return nil, err
	}
	pgPort, err := EnvInt("POSTGRES_PORT", 5432, 1, MaxPort)
	if err != nil {
		return nil, err
	}
	// 0 es no declararlo: TenantDirectDSN usa entonces POSTGRES_PORT.
	directPort, err := EnvInt("POSTGRES_DIRECT_PORT", 0, 0, MaxPort)
	if err != nil {
		return nil, err
	}
	accessTTL, err := EnvDuration("JWT_ACCESS_TTL", defaultAccessTTL, minAccessTTL, maxAccessTTL)
	if err != nil {
		return nil, err
	}
	refreshTTL, err := EnvDuration("JWT_REFRESH_TTL", defaultRefreshTTL, minRefreshTTL, maxRefreshTTL)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Postgres: PostgresConfig{
			Host:                        getEnv("POSTGRES_HOST", "localhost"),
			Port:                        pgPort,
			User:                        getEnv("POSTGRES_USER", "mail_admin"),
			Password:                    getEnv("POSTGRES_PASSWORD", ""),
			DBName:                      getEnv("POSTGRES_DB", "mail_registry"),
			DirectHost:                  getEnv("POSTGRES_DIRECT_HOST", ""),
			DirectPort:                  directPort,
			CellDBName:                  getEnv("CELL_DB_NAME", ""),
			CellUser:                    getEnv("CELL_DB_USER", ""),
			CellPassword:                getEnv("CELL_DB_PASSWORD", ""),
			AllowPlatformCellCredential: DeclaredDevelopmentOrTest(),
			RegistryUser:                getEnv("REGISTRY_DB_USER", ""),
			RegistryPassword:            getEnv("REGISTRY_DB_PASSWORD", ""),
			TenantUser:                  getEnv("TENANT_DB_USER", ""),
			TenantPassword:              getEnv("TENANT_DB_PASSWORD", ""),
		},
		Redis: redisCfg,
		NATS: NATSConfig{
			URL: getEnv("NATS_URL", "nats://localhost:4222"),
		},
		JWT: JWTConfig{
			AccessTTL:                accessTTL,
			RefreshTTL:               refreshTTL,
			AllowEphemeralSigningKey: DeclaredDevelopmentOrTest(),
		},
	}

	// Una contrasena de empresa sin su rol abriria las bases con la de plataforma creyendo
	// que usa la suya: eso no arranca. Al reves si vale y es el estado normal del reparto,
	// que va servicio a servicio: el rol declarado y la contrasena todavia sin publicar
	// dejan al servicio con la credencial de plataforma, y db.NewTenantRouting lo avisa.
	if cfg.Postgres.TenantPassword != "" && cfg.Postgres.TenantUser == "" {
		return nil, fmt.Errorf("TENANT_DB_PASSWORD is set without TENANT_DB_USER")
	}
	if cfg.Postgres.RegistryUser != "" && cfg.Postgres.RegistryPassword == "" {
		return nil, fmt.Errorf("REGISTRY_DB_USER is set without REGISTRY_DB_PASSWORD")
	}

	if cfg.Postgres.Password == "" && !cfg.Postgres.hasCellCredential() && !cfg.Postgres.HasServiceCredential() {
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
