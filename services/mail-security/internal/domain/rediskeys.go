package domain

// Claves de Redis que este servicio escribe y los motores leen (deploy/mail/README.md,
// contrato Redis). Nadie mas las escribe.
const (
	RedisDomainMap          = "DOMAIN_MAP"
	RedisRateLimitValue     = "RL_VALUE"
	RedisWhitelistedFwdHost = "WHITELISTED_FWD_HOST"
	RedisKeepSpam           = "KEEP_SPAM"
	RedisWantsSubjectTag    = "RCPT_WANTS_SUBJECT_TAG"
	RedisWantsSubfolderTag  = "RCPT_WANTS_SUBFOLDER_TAG"
	RedisQuarantineMaxSize  = "Q_MAX_SIZE"
	RedisQuarantineMaxAge   = "Q_MAX_AGE"
	RedisQuarantineRetain   = "Q_RETENTION_SIZE"
	RedisQuarantineExclude  = "Q_EXCLUDE_DOMAINS"
	RedisDKIMPrivKeys       = "DKIM_PRIV_KEYS"
	RedisDKIMSelectors      = "DKIM_SELECTORS"
	RedisRateLimitLog       = "RL_LOG"

	// SMTP_ACCESS de Rspamd: usuarios limitados y, por usuario, sus redes permitidas.
	RedisSMTPLimitedAccess   = "SMTP_LIMITED_ACCESS"
	RedisSMTPAllowNetsPrefix = "SMTP_ALLOW_NETS_"

	// Cortafuegos de la celda (netfilter). ACTIVE_BANS y PERM_BANS los escribe netfilter;
	// este servicio solo los lee y pide desbaneos por QUEUE_UNBAN.
	RedisF2BWhitelist  = "F2B_WHITELIST"
	RedisF2BBlacklist  = "F2B_BLACKLIST"
	RedisF2BOptions    = "F2B_OPTIONS"
	RedisF2BQueueUnban = "F2B_QUEUE_UNBAN"
	RedisF2BActiveBans = "F2B_ACTIVE_BANS"
	RedisF2BPermBans   = "F2B_PERM_BANS"
)

// SMTPAllowNetsKey es el hash de redes permitidas de un usuario limitado.
func SMTPAllowNetsKey(username string) string { return RedisSMTPAllowNetsPrefix + username }
