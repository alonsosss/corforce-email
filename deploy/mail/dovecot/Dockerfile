# gosu se compila aqui con el Go actual: el binario publicado (1.19) lleva la libreria estandar de Go
# 1.24.6, con una critica y veintiuna altas con arreglo; gosu solo cambia de usuario y ejecuta, asi que no
# las alcanza, pero un escaner las cuenta y no hay una version nueva publicada.
FROM golang:1.26-alpine AS gosu
# renovate: datasource=github-releases depName=tianon/gosu versioning=semver-coerced extractVersion=^(?<version>.*)$
ARG GOSU_VERSION=1.19
RUN apk add --no-cache git \
  && git clone --depth 1 --branch "$GOSU_VERSION" https://github.com/tianon/gosu /src \
  && cd /src \
  && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/gosu .

FROM alpine:3.21

LABEL maintainer="Core Force Mail"

ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8

# Add groups and users before installing Dovecot to not break compatibility
RUN addgroup -g 5000 vmail \
  && addgroup -g 401 dovecot \
  && addgroup -g 402 dovenull \
  && adduser -D -u 5000 -G vmail -h /var/vmail vmail \
  && adduser -D -G dovecot -u 401 -h /dev/null -s /sbin/nologin dovecot \
  && adduser -D -G dovenull -u 402 -h /dev/null -s /sbin/nologin dovenull \
  && apk add --no-cache --update \
  bash \
  bind-tools \
  findutils \
  envsubst \
  ca-certificates \
  curl \
  coreutils \
  jq \
  lua \
  lua-cjson \
  lua-socket \
  lua-sql-postgres \
  lua5.3-sql-postgres \
  icu-data-full \
  lua-sec \
  glib-dev \
  gcompat \
  postgresql-client \
  libproc2 \
  procps \
  python3 \
  py3-html2text \
  py3-jinja2 \
  py3-redis \
  redis \
  syslog-ng \
  syslog-ng-redis \
  syslog-ng-json \
  supervisor \
  tzdata \
  wget \
  dovecot \
  dovecot-dev \
  dovecot-lmtpd \
  dovecot-lua \
  dovecot-pgsql \
  dovecot-sql \
  dovecot-submissiond \
  dovecot-pigeonhole-plugin \
  dovecot-pop3d \
  dovecot-fts-flatcurve

COPY --from=gosu /out/gosu /usr/local/bin/gosu
RUN chmod +x /usr/local/bin/gosu && gosu nobody true

COPY trim_logs.sh /usr/local/bin/trim_logs.sh
COPY clean_q_aged.sh /usr/local/bin/clean_q_aged.sh
COPY syslog-ng.conf /etc/syslog-ng/syslog-ng.conf
COPY syslog-ng-redis_slave.conf /etc/syslog-ng/syslog-ng-redis_slave.conf
COPY report-spam.sieve /usr/lib/dovecot/sieve/report-spam.sieve
COPY report-ham.sieve /usr/lib/dovecot/sieve/report-ham.sieve
COPY rspamd-pipe-ham /usr/lib/dovecot/sieve/rspamd-pipe-ham
COPY rspamd-pipe-spam /usr/lib/dovecot/sieve/rspamd-pipe-spam
COPY sa-rules.sh /usr/local/bin/sa-rules.sh
COPY maildir_gc.sh /usr/local/bin/maildir_gc.sh
COPY docker-entrypoint.sh /
COPY supervisord.conf /etc/supervisor/supervisord.conf
COPY stop-supervisor.sh /usr/local/sbin/stop-supervisor.sh
COPY quota_notify.py /usr/local/bin/quota_notify.py
COPY repl_health.sh /usr/local/bin/repl_health.sh
COPY optimize-fts.sh /usr/local/bin/optimize-fts.sh
COPY crontab /etc/crontabs/root

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["/usr/bin/supervisord", "-c", "/etc/supervisor/supervisord.conf"]
