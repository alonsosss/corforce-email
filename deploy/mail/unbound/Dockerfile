FROM alpine:3.23
# Parches de la rama de Alpine publicados despues de la imagen base (UPSTREAM.md, seccion 9).
RUN apk upgrade --no-cache

LABEL maintainer = "Core Force Mail"

# install unbound from alpine:edge to get security patches
# minimum version is pinned; a failed index fetch makes apk fall back to the stable index, which
# cannot satisfy the pin, so the install alternates between two mirrors of the same signed index
RUN for intento in 1 2 3 4 5 6; do \
		case $((intento % 2)) in 0) espejo=https://uk.alpinelinux.org;; *) espejo=https://dl-cdn.alpinelinux.org;; esac; \
		apk add --no-cache --repository="$espejo/alpine/edge/main" "unbound>=1.26.1-r0" && exit 0; \
		sleep $((intento * 10)); \
	done; exit 1

# install other packages from regular alpine stable repo
RUN apk add --update --no-cache \
	curl \
	bind-tools \
	coreutils \
	bash \
	openssl \
	drill \
	tzdata \
	syslog-ng \
	supervisor \
	&& curl -o /etc/unbound/root.hints https://www.internic.net/domain/named.cache \
	&& chown root:unbound /etc/unbound \
    && adduser unbound tty \
	&& chmod 775 /etc/unbound

EXPOSE 53/udp 53/tcp

COPY docker-entrypoint.sh /docker-entrypoint.sh

# healthcheck (dig, ping)
COPY healthcheck.sh /healthcheck.sh
COPY syslog-ng.conf /etc/syslog-ng/syslog-ng.conf
COPY supervisord.conf /etc/supervisor/supervisord.conf
COPY stop-supervisor.sh /usr/local/sbin/stop-supervisor.sh

RUN chmod +x /healthcheck.sh
HEALTHCHECK --interval=30s --timeout=10s \
  CMD sh -c '[ -f /tmp/healthcheck_status ] && [ "$(cat /tmp/healthcheck_status)" -eq 0 ] || exit 1'

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["/usr/bin/supervisord", "-c", "/etc/supervisor/supervisord.conf"]
