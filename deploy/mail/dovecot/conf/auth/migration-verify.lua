-- Credencial de destino de un trabajo de migracion (docs/adr/0002). El ejecutor entra al buzon destino con
-- <buzon> y una contrasena que empieza por "cfmj1.": mail-migration la genero al reclamar el trabajo y solo
-- vale para ese buzon mientras el trabajo esta en curso. Aqui no se decide nada: se le pregunta a mail-auth
-- (service "migration"), que comprueba la red de origen, el token con mail-migration y el buzon.
--
-- Va antes de passwd-verify.lua y solo actua sobre contrasenas con ese prefijo; cualquier otra pasa de largo
-- sin llamar a nadie. Un rechazo tambien pasa de largo: el buzon que tenga una contrasena que empiece asi sigue
-- entrando por la passdb normal.
local PREFIX = "cfmj1."

function auth_password_verify(request, password)
  if request.domain == nil or request.service ~= "imap" or string.sub(password, 1, #PREFIX) ~= PREFIX then
    return dovecot.auth.PASSDB_RESULT_USER_UNKNOWN, ""
  end

  local json = require "cjson"
  local ltn12 = require "ltn12"
  local https = require "ssl.https"
  https.TIMEOUT = 15

  local req_json = json.encode({
    username = request.user,
    password = password,
    real_rip = request.real_rip,
    service = "migration"
  })
  local res = {}

  local _, c = https.request {
    method = "POST",
    url = "${MAIL_AUTH_URL}",
    source = ltn12.source.string(req_json),
    headers = {
      ["content-type"] = "application/json",
      ["content-length"] = tostring(#req_json)
    },
    sink = ltn12.sink.table(res),
    insecure = true
  }

  if c == 401 then
    return dovecot.auth.PASSDB_RESULT_PASSWORD_MISMATCH, "Job credential rejected"
  end
  if c ~= 200 then
    dovecot.i_info("Job credential check failed with " .. tostring(c) .. " for user " .. request.user)
    return dovecot.auth.PASSDB_RESULT_INTERNAL_FAILURE, "Upstream error"
  end

  local is_valid, response_json = pcall(json.decode, table.concat(res))
  if is_valid and response_json.success == true then
    return dovecot.auth.PASSDB_RESULT_OK, ""
  end
  return dovecot.auth.PASSDB_RESULT_PASSWORD_MISMATCH, "Job credential rejected"
end

function auth_passdb_lookup(req)
  return dovecot.auth.PASSDB_RESULT_USER_UNKNOWN, ""
end
