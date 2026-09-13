-- Schema: mail_security | Service: mail-security
--
-- Cortafuegos de la celda (contenedor netfilter): redes permitidas y denegadas y opciones
-- de baneo. netfilter las lee de Redis (F2B_WHITELIST, F2B_BLACKLIST, F2B_OPTIONS) y este
-- servicio, su unico escritor, las reconstruye desde aqui si Redis se vacia.
--
-- Son de la PLATAFORMA: el cortafuegos protege a todas las empresas de la celda y solo lo
-- opera el superadmin. Por eso no llevan tenant_id y mail_app no tiene permisos ni
-- politica: el servicio las lee y escribe como dueno, tras exigir el operador.
--
-- firewall_options es una sola fila por celda (scope = 'cell'); sin fila rigen los valores
-- por defecto de netfilter y este servicio no toca F2B_OPTIONS. Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail_security.firewall_networks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    list       text NOT NULL CHECK (list IN ('allow', 'deny')),
    network    cidr NOT NULL UNIQUE,
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Tiempos en segundos, como los espera netfilter/main.py; netban_* es el prefijo con el
-- que se banea la red de la IP infractora.
CREATE TABLE IF NOT EXISTS mail_security.firewall_options (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope              text NOT NULL UNIQUE DEFAULT 'cell' CHECK (scope = 'cell'),
    ban_time           integer NOT NULL CHECK (ban_time > 0),
    max_ban_time       integer NOT NULL,
    ban_time_increment boolean NOT NULL,
    max_attempts       integer NOT NULL CHECK (max_attempts > 0),
    retry_window       integer NOT NULL CHECK (retry_window > 0),
    netban_ipv4        integer NOT NULL CHECK (netban_ipv4 BETWEEN 8 AND 32),
    netban_ipv6        integer NOT NULL CHECK (netban_ipv6 BETWEEN 8 AND 128),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT firewall_options_ban_order CHECK (max_ban_time >= ban_time)
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['firewall_networks', 'firewall_options']
    LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
             WHERE tgname = 'trg_mail_security_' || t || '_updated_at'
               AND tgrelid = ('mail_security.' || t)::regclass
        ) THEN
            EXECUTE format('CREATE TRIGGER trg_mail_security_%I_updated_at BEFORE UPDATE ON mail_security.%I FOR EACH ROW EXECUTE FUNCTION update_updated_at()', t, t);
        END IF;
        EXECUTE format('REVOKE ALL ON mail_security.%I FROM mail_app', t);
        EXECUTE format('ALTER TABLE mail_security.%I ENABLE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;
