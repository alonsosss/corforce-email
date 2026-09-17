.PHONY: all build build-svc clean dev dev-down dev-logs dev-db test test-integration lint tidy deps

PROJECT=core-force-mail
GO=go
GOFLAGS=-ldflags="-s -w"

# La fuente de verdad de los servicios desplegables es docker-compose.yml (de ahi
# descubre ECR). Este target compila el monorepo completo como verificacion local.
all: build

build:
	$(GO) build ./...

# make build-svc name=<servicio>  (binario individual en bin/)
build-svc:
	$(GO) build $(GOFLAGS) -o bin/$(name) ./services/$(name)

clean:
	rm -rf bin/

dev:
	docker compose --profile embedded-db up --build -d

dev-down:
	docker compose --profile embedded-db down

dev-logs:
	docker compose logs -f

dev-db:
	docker compose --profile embedded-db up -d postgres-primary pgbouncer redis nats

test:
	$(GO) test ./... -race -count=1

# make test-integration  (pruebas //go:build integration contra Postgres y Redis desechables:
# una base por paquete, paquetes en serie, y una prueba que se salta cuenta como fallo;
# necesita docker. IT_PACKAGES acota los paquetes, IT_KEEP=1 deja los contenedores)
test-integration:
	@bash ops/scaffold/test-integration.sh

lint:
	golangci-lint run ./...

tidy:
	$(GO) mod tidy

deps:
	$(GO) mod download

# ── Scaffolding de plataforma ────────────────────────────────────────────────
.PHONY: new-service validate-scaffold gen-events

# make new-service name=domain-service port=8040 module=domains
# (omite module o usa module=--ungated para un servicio no gateado)
new-service:
	@ops/scaffold/new-service.sh $(name) $(port) $(module)

# make validate-scaffold  (colision de puertos + coherencia gateway/permisos/catalogo)
validate-scaffold:
	@ops/scaffold/validate.sh

# make gen-events  (regenera docs/arquitectura/EVENTS.md y EVENT-CONTRACTS.md desde el codigo:
# publicaciones directas y por outbox, suscripciones y campos)
gen-events:
	@ops/scaffold/gen-events.sh

# ── Migraciones ──────────────────────────────────────────────────────────────
.PHONY: check-migrations check-migration-drops

# make check-migrations  (las migraciones de empresa, celda y registro deben tolerar
# re-ejecutarse y no depender de un esquema que se crea despues)
check-migrations:
	@bash ops/scaffold/check-migrations.sh

# make check-migration-drops  (una migracion que dice reemplazar una restriccion y erra el
# nombre no falla: deja la vieja en pie y figura como aplicada)
check-migration-drops:
	@bash ops/scaffold/check-migration-drops.sh

# ── Guardarrailes de codigo ──────────────────────────────────────────────────
.PHONY: check-coupling check-silent-errors check-sql-arity check-streams check-base-images check-gofmt

# make check-coupling  (un servicio no lee tablas de otro contexto; solo vistas v_*)
check-coupling:
	@bash ops/scaffold/check-coupling.sh

# make check-silent-errors  (un 500 sin motivo registrado esconde el fallo)
check-silent-errors:
	@bash ops/scaffold/check-silent-errors.sh

# make check-sql-arity  (INSERT con distinto numero de columnas y valores)
check-sql-arity:
	@bash ops/scaffold/check-sql-arity.sh

# make check-streams  (dos streams de JetStream no pueden solaparse en subjects)
check-streams:
	@bash ops/scaffold/check-streams.sh

# make check-base-images  (imagenes fijadas por version o digest)
check-base-images:
	@bash ops/scaffold/check-base-images.sh

# make check-gofmt  (codigo Go sin gofmt, versionado o nuevo sin ignorar: el formato a mano
# se desalinea y cada gofmt posterior mete ruido en diffs ajenos)
check-gofmt:
	@out=$$(git ls-files --cached --others --exclude-standard -- '*.go' | \
		while IFS= read -r f; do [ -f "$$f" ] && printf '%s\n' "$$f"; done | xargs -r -d '\n' gofmt -l); \
		if [ -n "$$out" ]; then echo "sin gofmt (corre 'gofmt -w' sobre estos):" >&2; echo "$$out" >&2; exit 1; fi

# make gen-event-contracts (regenera docs/arquitectura/EVENT-CONTRACTS.md) /
# make check-event-contracts (drift + campo que un consumidor lee y su emisor no publica)
.PHONY: gen-event-contracts check-event-contracts
gen-event-contracts:
	@go run ./ops/scaffold/eventcontracts

check-event-contracts:
	@go run ./ops/scaffold/eventcontracts -check

# ── Operacion ────────────────────────────────────────────────────────────────
.PHONY: gen-observability-targets check-observability-targets check-alertas
.PHONY: check-secrets check-secret-sources check-db-credentials gen-compose-images check-compose-images
.PHONY: service-paths check-service-paths

# make gen-observability-targets  (regenera la lista de objetivos de Prometheus desde
# docker-compose.yml) / make check-observability-targets (falla si quedo desactualizada:
# un microservicio nuevo no puede nacer sin vigilancia)
gen-observability-targets:
	@bash ops/observability/gen-targets.sh

check-observability-targets:
	@bash ops/observability/gen-targets.sh >/dev/null
	@git diff --quiet -- ops/observability/prometheus/targets.json || \
		( echo "ops/observability/prometheus/targets.json desactualizado: corre 'make gen-observability-targets'" >&2; \
		  git --no-pager diff -- ops/observability/prometheus/targets.json; exit 1 )

# make check-alertas  (una alerta mal escrita no falla: se queda callada)
check-alertas:
	@bash ops/observability/check-alertas.sh

# make check-secrets  (ninguna credencial de ops/security/secrets/secret-keys.txt puede
# tener valor en un fichero versionado)
check-secrets:
	@bash ops/security/secrets/check-secrets.sh

# make check-secret-sources  (ningun script se busca un secreto en el .env)
check-secret-sources:
	@bash ops/security/secrets/check-secret-sources.sh

# make check-db-credentials  (cada servicio recibe SU credencial de base y ninguna otra:
# compose, el almacen y los permisos del rol dicen lo mismo)
check-db-credentials:
	@bash ops/scaffold/check-db-credentials.sh

# make gen-compose-images  (regenera los overrides de imagenes: ECR y transporte save) /
# make check-compose-images (falla si un servicio con build quedo fuera de alguno)
gen-compose-images:
	@bash ops/ecr/gen-compose-images.sh

check-compose-images:
	@bash ops/ecr/gen-compose-images.sh >/dev/null
	@git diff --quiet -- docker-compose.images.yml docker-compose.images.save.yml || \
		( echo "los overrides de imagenes estan desactualizados: corre 'make gen-compose-images'" >&2; \
		  git --no-pager diff -- docker-compose.images.yml docker-compose.images.save.yml; exit 1 )

# make check-compose  (los ficheros de compose tienen que ser validos para docker compose;
# sin docker avisa y no falla)
check-compose:
	@bash ops/scaffold/check-compose.sh

# make check-service-paths  (todo servicio con build: debe quedar en el mapa ruta->servicio)
service-paths:
	@bash ops/scaffold/service-paths.sh

check-service-paths:
	@bash ops/scaffold/service-paths.sh --check

# ── Agregados ────────────────────────────────────────────────────────────────
.PHONY: checks clean-copy e2e check-compose

# make e2e  (binarios reales contra Postgres, NATS y Redis desechables: plataforma vacia,
# empresa, acceso por el gateway, correo en la celda, Redis de los motores, plantillas y
# supresion; necesita docker y termina con error si un paso no cuadra)
e2e:
	@bash ops/e2e/run.sh

# make e2e-mail  (los motores de deploy/mail contra una celda real: Postfix, Dovecot, Rspamd,
# ClamAV, Unbound y redis-mail con mail-auth, mail-policy y el webmail; mapas, IMAP, envio
# autenticado, antivirus y webmail por el gateway. Necesita docker y red; E2E_KEEP=1 deja
# todo en pie)
e2e-mail:
	@bash ops/e2e/mail.sh

# make checks  (todo lo que corre CI sin docker, en un solo comando; si CI anade un paso,
# se anade aqui: un check que solo corre en CI deja pasar lo que rompe el despliegue)
checks: build check-gofmt check-migrations check-migration-drops check-coupling check-silent-errors \
	check-sql-arity check-streams check-event-contracts check-secrets check-secret-sources \
	check-db-credentials check-compose check-compose-images check-observability-targets \
	check-service-paths check-alertas validate-scaffold
	@$(GO) vet ./...
	@echo "checks: OK"

# make clean-copy  (no queda ningun resto de las bases de referencia en el codigo)
clean-copy:
	@bash ops/scaffold/check-clean-copy.sh
