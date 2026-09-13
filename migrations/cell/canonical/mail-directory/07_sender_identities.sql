-- Schema: mail | Service: mail-directory
--
-- Quien puede enviar como quien, en UN solo sitio. Postfix lo decidia con la consulta que
-- postfix.sh escribia en pgsql_virtual_sender_acl.cf (smtpd_sender_login_maps) y el webmail
-- necesita la respuesta inversa: que remitentes puede ofrecer a un buzon. Dos copias de la
-- misma regla acabarian diciendo cosas distintas y el webmail ofreceria remitentes que
-- Postfix rechaza. Por eso la regla vive aqui y los dos la usan:
--
--   mail.sender_login_owners(remitente)  los nombres SASL duenos del remitente. Es la
--       consulta que tenia Postfix sin cambiar su logica (tampoco la precedencia del AND
--       final de sender_acl); el mapa de Postfix llama ahora a esta funcion con '%s'.
--   mail.sender_identities(buzon)        las direcciones CONCRETAS con las que el buzon puede
--       enviar: los candidatos (el propio buzon, sus variantes en dominios alias, los aliases
--       que lo tienen de destino y las filas de sender_acl) que sender_login_owners acepta.
--       Lo que la regla de Postfix no acepta no sale. Los permisos comodin (alias catch-all,
--       '@dominio' o '*' en sender_acl) no se enumeran: son infinitos, y el webmail solo
--       ofrece y admite direcciones concretas.
--
-- Postfix pasa la clave en minusculas y no consulta sin parte local o sin dominio (%u o %d
-- vacios suprimen la consulta); la funcion hace lo mismo. El dueno se compara sin distinguir
-- mayusculas y la lista de duenos se parte por comas y espacios, como hace Postfix.
--
-- SECURITY INVOKER a proposito: cada rol lee con sus propios permisos y politicas
-- (mail_engine con engine_read, mail_service con service_all). Idempotente y aditiva.

CREATE OR REPLACE FUNCTION mail.sender_login_owners(p_sender text)
RETURNS SETOF text
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_at     integer;
    v_local  text;
    v_domain text;
BEGIN
    IF p_sender IS NULL OR strpos(p_sender, '@') = 0 THEN
        RETURN;
    END IF;
    v_at := length(p_sender) - strpos(reverse(p_sender), '@') + 1;
    v_local := left(p_sender, v_at - 1);
    v_domain := substr(p_sender, v_at + 1);
    IF v_local = '' OR v_domain = '' THEN
        RETURN;
    END IF;

    RETURN QUERY
    SELECT a.goto FROM mail.aliases a
     WHERE a.id IN (
            SELECT COALESCE(
                (SELECT x.id FROM mail.aliases x
                  WHERE x.address = p_sender AND x.active IN (1, 2) AND x.sender_allowed),
                (SELECT x.id FROM mail.aliases x
                  WHERE x.address = '@' || v_domain AND x.active IN (1, 2) AND x.sender_allowed)))
       AND a.active = 1
       AND a.sender_allowed
       AND (a.domain IN (SELECT d.domain FROM mail.domains d WHERE d.domain = v_domain AND d.active)
            OR a.domain IN (SELECT ad.alias_domain FROM mail.alias_domains ad
                             WHERE ad.alias_domain = v_domain AND ad.active))
    UNION
    SELECT s.logged_in_as FROM mail.sender_acl s
     WHERE s.send_as = '@' || v_domain
        OR s.send_as = p_sender
        OR s.send_as = '*'
        OR s.send_as IN (SELECT '@' || ad.target_domain FROM mail.alias_domains ad
                          WHERE ad.alias_domain = v_domain)
        OR s.send_as IN (SELECT v_local || '@' || ad.target_domain FROM mail.alias_domains ad
                          WHERE ad.alias_domain = v_domain)
           AND s.logged_in_as NOT IN (SELECT x.goto FROM mail.aliases x WHERE x.address = p_sender)
    UNION
    SELECT m.username FROM mail.mailboxes m
     WHERE m.username = p_sender AND m.active = 1
    UNION
    SELECT m.username FROM mail.mailboxes m, mail.alias_domains ad
     WHERE ad.alias_domain = v_domain
       AND m.username = v_local || '@' || ad.target_domain
       AND m.active IN (1, 2)
       AND ad.active;
END
$$;

CREATE OR REPLACE FUNCTION mail.sender_identities(p_login text)
RETURNS SETOF text
LANGUAGE sql STABLE
SET search_path = pg_catalog, pg_temp
AS $$
    WITH candidates(address) AS (
        SELECT m.username FROM mail.mailboxes m
         WHERE m.username = lower(p_login)
        UNION
        SELECT m.local_part || '@' || ad.alias_domain
          FROM mail.mailboxes m JOIN mail.alias_domains ad ON ad.target_domain = m.domain
         WHERE m.username = lower(p_login)
        UNION
        SELECT a.address FROM mail.aliases a
         WHERE a.address NOT LIKE '@%'
           AND strpos(lower(a.goto), lower(p_login)) > 0
           AND lower(p_login) IN (SELECT lower(g.name)
                                    FROM regexp_split_to_table(a.goto, '[,[:space:]]+') AS g(name))
        UNION
        SELECT s.send_as FROM mail.sender_acl s
         WHERE lower(s.logged_in_as) = lower(p_login)
           AND s.send_as LIKE '_%@_%'
        UNION
        SELECT substring(s.send_as FROM '^(.*)@[^@]*$') || '@' || ad.alias_domain
          FROM mail.sender_acl s
          JOIN mail.alias_domains ad ON ad.target_domain = substring(s.send_as FROM '@([^@]*)$')
         WHERE lower(s.logged_in_as) = lower(p_login)
           AND s.send_as LIKE '_%@_%'
    )
    SELECT DISTINCT lower(c.address) FROM candidates c
     WHERE EXISTS (
            SELECT 1
              FROM mail.sender_login_owners(lower(c.address)) AS o(owner),
                   regexp_split_to_table(o.owner, '[,[:space:]]+') AS n(name)
             WHERE lower(n.name) = lower(p_login))
$$;

REVOKE ALL ON FUNCTION mail.sender_login_owners(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION mail.sender_identities(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mail.sender_login_owners(text) TO mail_engine, mail_service;
GRANT EXECUTE ON FUNCTION mail.sender_identities(text) TO mail_service;
