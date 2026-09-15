# Aplicacion web de Core Force Mail: se compila con pnpm y se sirve con nginx en el puerto
# 80 de la red interna. El gateway es la unica entrada: sirve esta app en /* y el API en
# /api/v1 del mismo origen, y pone la CSP con nonce y el resto de cabeceras de seguridad.

FROM node:24-alpine AS build

WORKDIR /app

COPY package.json pnpm-lock.yaml ./

# La version de pnpm sale de "packageManager" en package.json, la misma que usa el CI.
RUN corepack enable && corepack prepare --activate

# --frozen-lockfile: se despliega exactamente el arbol de dependencias que valido el CI.
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store

COPY . .

# Vacio = mismo origen (lo normal detras del gateway). Solo se fija si el API vive en otro host.
ARG VITE_API_URL=
ENV VITE_API_URL=${VITE_API_URL}

RUN pnpm build

FROM nginx:1.28-alpine

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

# Sello del build: hash del contenido compilado, no de la fecha ni del commit. Dos builds
# identicos dan el mismo sello, asi que un redespliegue sin cambios no anuncia una version
# nueva que no existe.
RUN cd /usr/share/nginx/html && \
    printf '{"build":"%s"}\n' \
      "$(find . -type f ! -name version.json -exec sha256sum {} + | sort | sha256sum | cut -c1-16)" \
      > version.json

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1/version.json || exit 1
