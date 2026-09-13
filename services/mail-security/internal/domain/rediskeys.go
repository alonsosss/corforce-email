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
)
