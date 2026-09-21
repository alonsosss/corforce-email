FROM alpine:3.21

LABEL maintainer = "Core Force Mail"

ARG PIP_BREAK_SYSTEM_PACKAGES=1
WORKDIR /app

#RUN addgroup -S olefy && adduser -S olefy -G olefy \
RUN apk add --virtual .build-deps gcc musl-dev python3-dev libffi-dev openssl-dev cargo \
  && apk add --update --no-cache python3 py3-pip openssl tzdata libmagic \
  && pip3 install --upgrade pip \
  && pip3 install --upgrade asyncio python-magic \
  && pip3 install --upgrade https://github.com/decalage2/oletools/archive/master.zip \
  && pip3 install --upgrade "setuptools>=78.1.1" \
  && pip3 uninstall -y pip \
  && apk del .build-deps
#  && sed -i 's/template_injection_detected = True/template_injection_detected = False/g' /usr/lib/python3.9/site-packages/oletools/olevba.py

ADD olefy.py /app/

RUN chown -R nobody:nobody /app /tmp

USER nobody

CMD ["python3", "-u", "/app/olefy.py"]
