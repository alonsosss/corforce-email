FROM golang:1.25-bookworm AS builder
WORKDIR /src

ENV CGO_ENABLED=0 \
    GO111MODULE=on \
		NOOPT=1 \
    VERSION=1.8.22

# golang.org/x/net v0.47.0, que fija v1.8.22, tiene cinco vulnerabilidades altas con arreglo y este
# servicio descarga politicas MTA-STS de dominios ajenos: se sube antes de compilar.
RUN git clone --branch v${VERSION} https://github.com/Zuplu/postfix-tlspol && \
    cd /src/postfix-tlspol && \
    go get golang.org/x/net@v0.56.0 && go mod tidy && go mod vendor && \
    scripts/build.sh build-only


FROM debian:bookworm-slim
LABEL maintainer="Core Force Mail"

ARG DEBIAN_FRONTEND=noninteractive
ENV LC_ALL=C

RUN apt-get update && apt-get install -y --no-install-recommends \
	ca-certificates \
	dirmngr \
  	dnsutils \
	iputils-ping \
	sudo \
	supervisor \
	redis-tools \
	syslog-ng \
	syslog-ng-core \
	syslog-ng-mod-redis \
  	tzdata \
	&& rm -rf /var/lib/apt/lists/* \
	&& touch /etc/default/locale

COPY supervisord.conf /etc/supervisor/supervisord.conf
COPY syslog-ng.conf /etc/syslog-ng/syslog-ng.conf
COPY syslog-ng-redis_slave.conf /etc/syslog-ng/syslog-ng-redis_slave.conf
COPY postfix-tlspol.sh /opt/postfix-tlspol.sh
COPY stop-supervisor.sh /usr/local/sbin/stop-supervisor.sh
COPY docker-entrypoint.sh /docker-entrypoint.sh
COPY --from=builder /src/postfix-tlspol/build/postfix-tlspol /usr/local/bin/postfix-tlspol

RUN chmod +x /opt/postfix-tlspol.sh \
  /usr/local/sbin/stop-supervisor.sh \
  /docker-entrypoint.sh
RUN rm -rf /tmp/* /var/tmp/*

ENTRYPOINT ["/docker-entrypoint.sh"]

CMD ["/usr/bin/supervisord", "-c", "/etc/supervisor/supervisord.conf"]