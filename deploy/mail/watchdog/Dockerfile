FROM alpine:3.23

LABEL maintainer = "Core Force Mail"

# Installation
RUN apk add --update \
  && apk add --no-cache nagios-plugins-smtp \
  nagios-plugins-tcp \
  nagios-plugins-http \
  nagios-plugins-ping \
  curl \
  bash \
  coreutils \
  jq \
  fcgi \
  openssl \
  nagios-plugins-disk \
  bind-tools \
  redis \
  perl \
  perl-net-dns \
  perl-io-socket-ssl \
  perl-io-socket-inet6 \
  perl-socket \
  perl-socket6 \
  perl-mime-lite \
  perl-term-readkey \
  tini \
  tzdata \
  whois \
  && curl https://raw.githubusercontent.com/mludvig/smtp-cli/v3.10/smtp-cli -o /smtp-cli \
  && chmod +x smtp-cli \
  && mkdir /usr/lib/platform

COPY watchdog.sh /watchdog.sh
COPY check_dns.sh /usr/lib/platform/check_dns.sh

CMD ["/watchdog.sh"]
