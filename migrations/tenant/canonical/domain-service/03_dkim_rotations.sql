-- Schema: domains | Service: domain-service
--
-- Ciclo de vida de las claves DKIM de un dominio:
--   dkim_previous_signed_at: ultima vez que la clave anterior pudo firmar. La gracia de una
--     rotacion programada se cuenta desde aqui, no desde la rotacion: un mensaje firmado con
--     ella puede seguir en la cola de Postfix hasta maximal_queue_lifetime, y su TXT no puede
--     retirarse antes. Mientras el TXT de la clave nueva no se ve publicado se sigue firmando
--     con la anterior y esta marca avanza.
--   dkim_confirmed_at: cuando una verificacion vio publicado por primera vez el TXT de la clave
--     actual. Nula tras rotar o revocar; un dominio verificado cuya clave aun no se vio
--     publicada no cae a failed solo por el DKIM (lo cambio la plataforma, no el cliente).
--   dkim_revocation_pending: una revocacion por clave comprometida se guardo y la celda aun no
--     confirmo que sus motores solo tienen la clave nueva. El barrido y el reintento de la
--     peticion la repiten hasta que la confirma.
-- domains.dkim_rotations: historial de rotaciones y revocaciones con su motivo y quien las
-- pidio. Solo se inserta; cae con el dominio.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS dkim_previous_signed_at timestamptz;
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS dkim_confirmed_at timestamptz;
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS dkim_revocation_pending boolean NOT NULL DEFAULT false;

-- Una clave en gracia de antes de esta migracion firmo por ultima vez, como pronto, al rotar.
UPDATE domains.domains SET dkim_previous_signed_at = dkim_rotated_at
 WHERE dkim_previous_selector IS NOT NULL AND dkim_previous_signed_at IS NULL;

-- Un dominio verificado sin clave en gracia tiene publicado el TXT de su clave actual: sin el no
-- habria verificado.
UPDATE domains.domains SET dkim_confirmed_at = COALESCE(verified_at, updated_at)
 WHERE status = 'verified' AND dkim_previous_selector IS NULL AND dkim_confirmed_at IS NULL;

-- El barrido busca las revocaciones pendientes de cada empresa; casi nunca hay ninguna.
CREATE INDEX IF NOT EXISTS idx_domains_revocation_pending ON domains.domains (tenant_id)
    WHERE dkim_revocation_pending;

-- kind: scheduled conserva la clave anterior durante la gracia (previous_selector);
-- compromised retira de inmediato todas las que tenia el dominio (revoked_selectors) y exige
-- un motivo. actor_id es el usuario que la pidio.
CREATE TABLE IF NOT EXISTS domains.dkim_rotations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    domain_id uuid NOT NULL REFERENCES domains.domains(id) ON DELETE CASCADE,
    kind varchar(20) NOT NULL,
    selector varchar(63) NOT NULL,
    previous_selector varchar(63),
    revoked_selectors text[] NOT NULL DEFAULT '{}',
    reason text NOT NULL DEFAULT '',
    actor_id uuid,
    rotated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dkim_rotations_kind_check CHECK (kind IN ('scheduled', 'compromised')),
    CONSTRAINT dkim_rotations_compromised_reason CHECK (kind <> 'compromised' OR (reason <> '' AND cardinality(revoked_selectors) > 0))
);

CREATE INDEX IF NOT EXISTS idx_dkim_rotations_domain_rotated ON domains.dkim_rotations (domain_id, rotated_at DESC);
