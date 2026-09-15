dns {
  enable_dnssec = true;
}
map_watch_interval = 30s;
task_timeout = 30s;
enable_mime_utf = true;
disable_monitoring = true;
# In case a task times out (like DNS lookup), soft reject the message
# instead of silently accepting the message without further processing.
soft_reject_on_timeout = true;
local_addrs = /etc/rspamd/custom/platform_networks.map;
# Mayor mensaje que Rspamd analiza, en bytes: message_size_limit de Postfix (104857600) mas 1 MiB
# por las cabeceras que reconstruye el milter. Uno mayor recibe tempfail del proxy del milter y
# Postfix no llega a entregarlo; el defecto de rspamd 4.1.4 (50 MiB) dejaba asi todo mensaje de 50 a
# 100 MiB. ops/scaffold/check-mail-size-limits.sh lo compara con Postfix y con mail-security.
max_message = 105906176;
