# Core Force Mail: aplicacion web

Una sola aplicacion React 18 + TypeScript (estricto) + Vite 5, sin module federation. Es el
plano de control: acceso, cuenta, usuarios, roles y permisos, sesiones, empresas, celdas y
auditoria. Las pantallas de correo (dominios, buzones, aliases) llegan en una segunda ola.

## Desarrollo

Requisitos: Node 20 y pnpm 9 (`corepack enable` toma la version de `packageManager`).

```bash
cd web
cp .env.example .env.local   # opcional: solo si el gateway no esta en 127.0.0.1:8080
pnpm install
pnpm dev                     # http://localhost:3000
```

Vite reenvia `/api` al gateway (`VITE_DEV_PROXY_TARGET`, por defecto
`http://127.0.0.1:8080`). El navegador ve un solo origen, igual que en produccion, y la
cookie del refresh viaja sin excepciones de CORS. El gateway local debe correr con
`AUTH_COOKIE_SECURE=false`: en `http://localhost` una cookie `Secure` no se guarda.

Comprobaciones (las mismas que el job `web` del CI):

```bash
pnpm install --frozen-lockfile
pnpm lint
pnpm typecheck
pnpm test -- --run
pnpm build
```

## Produccion

`Dockerfile` compila con pnpm y sirve `dist/` con nginx en el puerto 80 de la red
`mail-internal`. El gateway sirve la app en `/*` (servicio `web` de
`services/gateway/routes.json`) y el API en `/api/v1` del mismo origen, estampa el nonce de
la CSP en los `<script>` de `index.html` y pone las cabeceras de seguridad.

`nginx.conf` cachea por nombre de fichero: `index.html` y `version.json` con `no-store`,
los ficheros con hash de contenido con `immutable`, y el resto revalidando. `version.json`
es el hash del contenido compilado.

## Sesion y seguridad

Detalle en `docs/arquitectura/CSP-Y-SESION.md`.

- El refresh token vive en la cookie `cf_rt` (HttpOnly, Path=/api/v1/auth). Login, reto
  MFA y renovacion mandan `cookie_auth: true`, asi que nunca aparece en el JSON.
- El access token vive solo en memoria (propiedad no enumerable de `window`). Nada de la
  sesion toca `localStorage`: ESLint lo prohibe y la unica excepcion es el tema visual
  (`src/design/theme.ts`).
- `src/api/client.ts`: renovacion de vuelo unico 60 s antes de vencer, un 401 provoca una
  renovacion y un reintento, y un 403 `STEP_UP_REQUIRED` abre el modal de step-up y
  reintenta con `X-Step-Up`. Al cargar, `hydrate()` restaura la sesion con la cookie.
- CSP con nonce y `strict-dynamic`: el build no emite scripts inline ni
  `<link rel="modulepreload">` en el HTML; las pantallas se cargan con `import()`.

## Estructura

```
src/
  api/          client.ts (fetch, sesion, step-up), endpoints.ts (TODAS las rutas del API),
                errors.ts (ApiError con code), messages.ts (codigo -> texto),
                identity.ts, access.ts, organization.ts, audit.ts (DTO de los handlers Go)
  auth/         store de sesion (hydrate, login, completeMfa, logout), StepUpModal
  access/       useAccess() con modules, roles, isAdmin y can(module, resource, action);
                modules.ts, roles.ts y permissions.ts (triples del catalogo sembrado)
  i18n/         es.ts con todos los textos; t('clave', {vars})
  design/       tokens.css (claro y oscuro por [data-theme]), components.css, componentes
                propios (Button, Input, Select, Checkbox, DataTable, Modal, Toast, Tabs,
                Badge, EmptyState, ErrorState, Skeleton, PageHeader, FormField,
                ConfirmDialog) e icons/ (un SVG por fichero, stroke="currentColor")
  layout/       Shell, Sidebar, Topbar, nav.ts (declaracion NAV filtrada por modulos)
  pages/        una carpeta por modulo
  hooks/        useQuery, useAction, usePagination
  lib/          fechas, validacion, reglas de contrasena, lectura de claims
  paths.ts      rutas de la aplicacion
  routes.tsx    pantallas con React.lazy y el modulo o rol que exige cada una
```

## Anadir un modulo (por ejemplo, correo)

1. Rutas del API en `src/api/endpoints.ts` y funciones tipadas en `src/api/<modulo>.ts`
   con los DTO exactos del handler Go.
2. El modulo de permiso en `src/access/modules.ts` (el mismo valor que la columna `module`
   de `access_control.permissions` y que `services/gateway/routes.json`) y sus triples en
   `src/access/permissions.ts`.
3. La ruta en `src/paths.ts`, la pantalla en `src/routes.tsx` con su `module`, y la entrada
   del menu en `src/layout/nav.ts` con el mismo `module`. `nav.test.ts` falla si el menu y
   las rutas no coinciden.
4. Los textos en `src/i18n/es.ts`.
