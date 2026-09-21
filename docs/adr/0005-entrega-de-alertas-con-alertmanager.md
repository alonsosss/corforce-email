# ADR 0005: Entrega de las alertas por correo con Alertmanager

## Estado

Implementado y verificado en producción (2026-09-21).

## Contexto

Prometheus evalúa las reglas de `ops/observability/prometheus/rules/plataforma.yml`, pero no avisa a nadie: una
alerta disparada solo se ve consultando Prometheus por túnel SSH. El diseño heredado del ERP entregaba las alertas
por un servicio `notification` que esta plataforma no copió (`Organizacion_Repositorio.md`), de modo que sus reglas
de vigilancia (`VigiaDeAlertasNoSeRegistra`, `AlertasSinDestinatario`) esperaban una serie que nadie emite. Sin un
camino de entrega, una caída de un servicio o un disco al límite se descubre por un usuario, no por el sistema.

## Alternativas

1. **Alertmanager integrado de Grafana** (Prometheus lo usa como `alertmanagers`). Descartada: la ruta
   `/api/alertmanager/grafana/api/v2/alerts` de Grafana 11.6 devuelve 400 al formato con el que Prometheus envía las
   alertas (`json: cannot unmarshal array into Go value of type definitions.PostableAlerts`), comprobado con
   Prometheus real. Además exigía un usuario de Grafana con clave para Prometheus.
2. **Servicio propio de notificaciones en Go.** Descartada: reimplementa agrupación, deduplicación y reenvío que
   Alertmanager ya resuelve, y sería código de producción sin necesidad.
3. **Alertmanager de Prometheus** (elegida): es el componente estándar del mismo ecosistema que ya se adoptó
   (Prometheus, Grafana, Loki), es una sola imagen sin dependencias y no publica puerto.

## Decisión

Un contenedor `alertmanager` en `docker-compose.observability.yml`, junto a Prometheus y sin puerto publicado.
Prometheus le entrega las alertas por la red interna. Alertmanager las agrupa por `alertname` e `instance`, espera
30 s, reenvía cada 4 h mientras dure la causa y avisa también de la resolución. El destino es un correo enviado por
el submission de la propia plataforma con STARTTLS obligatorio y certificado verificado.

* La configuración es una plantilla (`ops/observability/alertmanager/alertmanager.yml.tmpl`) porque Alertmanager no
  expande variables de entorno. `render.sh` sustituye tres valores no secretos (`ALERT_EMAIL_TO`,
  `ALERT_SMTP_HOST`, `ALERT_SMTP_USER`) y rechaza cualquier carácter que no sea de una dirección o de un
  `host:puerto`, porque van entre comillas simples en YAML.
* La clave SMTP es un secreto: se lee de un fichero (`smtp_auth_password_file`), montado suelto y 0644 dentro de un
  directorio 0700. El contenedor corre como `nobody` y solo debe ver ese fichero.
* `check-alertas.sh` renderiza la plantilla con la misma imagen y la valida con `amtool`, y comprueba que un valor
  con comilla se rechaza.
* Se retiraron las reglas que dependían de emisores inexistentes (chat, vigilancia de `notification`, parcheado del
  host): no había ninguna serie que las alimentara y solo daban alertas críticas falsas.

## Consecuencias

* Con la plataforma caída del todo, Postfix también lo está y el aviso no sale. `ALERT_EMAIL_TO` debe ser una
  dirección externa a los dominios de la plataforma; para la caída total hace falta además un canal que no dependa
  de este servidor (Telegram, un latido externo), que necesita credenciales que hoy no existen.
* Un contenedor más (128 MB de tope) y un volumen para las silencios y el estado de notificaciones.
* Una alerta sin destinatario es peor que ninguna: el compose no arranca sin `ALERT_EMAIL_TO`.
