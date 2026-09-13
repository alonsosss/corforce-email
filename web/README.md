# Core Force Mail: aplicacion web

Una sola aplicacion React 18 + TypeScript (estricto) + Vite 5, sin module federation. Cubre
el plano de control (acceso, cuenta, usuarios, roles y permisos, sesiones, empresas, celdas
y auditoria), el correo corporativo (dominios, directorio de la celda, buzones, enrutado,
seguridad y cuarentena), los envios (plantillas, supresion y reputacion), el marketing
(contactos, segmentos, campanas y analitica) y el plan de la empresa. El superadmin opera
ademas el catalogo de planes, las suscripciones y la reputacion de todas las empresas.

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

## Correo y envios

| Menu                                    | Ruta                                           | Modulo          | API                         |
| --------------------------------------- | ---------------------------------------------- | --------------- | --------------------------- |
| Dominios (y directorio, dominios alias) | `/mail/domains`, `/mail/domains/:id`           | `domains`       | `/domains`, `/mail-domains` |
| Buzones                                 | `/mail/mailboxes`, `/mail/mailboxes/:id`       | `mailboxes`     | `/mailboxes`                |
| Enrutado                                | `/mail/routing`                                | `mail_routing`  | `/mail-routing/*`           |
| Seguridad                               | `/mail/security`                               | `mail_security` | `/mail-security/*`          |
| Cuarentena                              | `/mail/quarantine`                             | `mail_security` | `/mail-security/quarantine` |
| Plantillas                              | `/sending/templates`, `/sending/templates/:id` | `templates`     | `/templates`                |
| Supresion                               | `/sending/suppression`                         | `suppression`   | `/suppression`              |
| Reputacion                              | `/sending/reputation`                          | `reputation`    | `/reputation`               |

## Marketing, plan y plataforma

| Menu                                    | Ruta                                                                                                                    | Modulo o rol     | API                                |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- | ---------------- | ---------------------------------- |
| Contactos (listas, atributos, CSV)      | `/marketing/contacts`, `/marketing/contacts/:id`, `/marketing/lists/:id`                                                | `contacts`       | `/contacts/*`                      |
| Segmentos                               | `/marketing/segments`, `/marketing/segments/new`, `/marketing/segments/:id`                                             | `segments`       | `/segments/*`                      |
| Campanas                                | `/marketing/campaigns`, `/marketing/campaigns/:id`                                                                      | `campaigns`      | `/campaigns/*`                     |
| Automatizaciones (flujos, doble opt-in) | `/marketing/automations`, `/marketing/automations/new`, `/marketing/automations/:id`, `/marketing/automations/runs/:id` | `automations`    | `/automations/*`                   |
| Analitica                               | `/marketing/analytics`                                                                                                  | `analytics`      | `/analytics/*`                     |
| Plan y consumo                          | `/billing`                                                                                                              | `billing`        | `/billing/subscription`, `/usage`  |
| Planes y suscripciones (plataforma)     | `/platform/billing`                                                                                                     | rol `superadmin` | `/billing/plans`, `/subscriptions` |
| Reputacion de empresas (plataforma)     | `/platform/reputation`                                                                                                  | rol `superadmin` | `/reputation/tenants/*`            |

Las dos pantallas de plataforma se gatean por rol del sistema y no por modulo: billing
`plans`/`subscriptions` y reputation `tenants` tienen alcance plataforma y el servicio exige
el rol superadmin. Suspender, liberar y fijar limites de reputacion son acciones criticas:
si el API responde `STEP_UP_REQUIRED`, el cliente pide reconfirmar la identidad y reintenta.

Catalogos del API en lugar de constantes copiadas:

- `GET /templates/meta`: tipos, estados, estados de version, tipos de variable, variables
  reservadas y topes (`templatesMeta` en `api/templates.ts`).
- `GET /suppression/meta`: motivos con su gravedad y si un operador puede retirarlos,
  motivos del alta manual y topes de consulta, importacion y longitud.
- `GET /segments/meta`: campos, operadores con su aridad, valores de los enumerados,
  atributos declarados de la empresa y limites del DSL. El editor de segmentos se construye
  solo con esto.
- `GET /contacts/meta`: estados de contacto y de consentimiento, lo que la empresa puede
  registrar por API, tipos de atributo y topes de importacion y de pagina.
- `GET /campaigns/meta`: estados con las acciones que admite cada uno (la pantalla no tiene
  reglas propias de transicion), destinatarios de prueba y tamano de lote efectivo.
- `GET /automations/meta`: estados con sus acciones, disparadores, tipos de paso, unidades y
  topes de espera, tipo de plantilla de cada uso y tope efectivo del doble opt-in.
- `GET /billing/meta`: recursos con su tipo (el alta de un plan lleva un limite por
  recurso), periodos, estados y el valor de included que significa sin limite.
- `GET /reputation/meta`: estados de menos a mas grave, clases de envio, motivos y topes.
- `GET /mail-directory/meta`: estados activos, politicas TLS, tipos de mapa BCC, longitudes
  de contrasena y de nombre, tamano de sieve y pagina maxima del directorio.

Se leen con `cachedResource` (una peticion por sesion) y `useResource`. La unica lista que
queda copiada es la de clases de envio del filtro de analitica (`api/sendClass.ts`):
analytics no publica catalogo y el de reputation pertenece a otro modulo de permisos.

Reglas de la interfaz que no se relajan:

- HTML que no es de la aplicacion (plantillas, pies de pagina, aviso de cuarentena) solo se
  pinta en `HtmlPreviewFrame`: `<iframe sandbox="">` con `srcdoc`, sin scripts, formularios,
  navegacion ni mismo origen. Nunca `dangerouslySetInnerHTML`. Antes de `srcdoc`,
  `lib/untrustedHtml.ts` quita todo lo que carga algo por si solo (link, base, meta
  http-equiv, iframes, objetos, audio y video), reescribe las imagenes remotas (`src`,
  `srcset`, `background`, `url()` en estilos, `image` de SVG) y anade una CSP propia al
  documento. Las imagenes remotas solo se muestran si quien mira pulsa "Mostrar imagenes
  remotas" en esa vista: un pixel de seguimiento no sabe quien abre la vista previa.
- Importes, tasas y porcentajes del API son texto decimal: se muestran con `lib/decimal.ts`
  desplazando la coma en la cadena, sin recalcularlos ni pasarlos por float.
- La exportacion del titular (`GET /contacts/{id}/export`) solo se pide al pulsar el boton y
  se queda en memoria hasta descargarla o descartarla.
- El mensaje en cuarentena (`message/rfc822`) se pide con `api.getText` y se muestra como
  texto en un `<pre>`; jamas se interpreta su HTML.
- Las contrasenas de aplicacion se muestran una sola vez, en un dialogo que no se cierra
  por accidente, y se descartan del estado al cerrarlo. Las de relayhosts y transportes
  son de solo escritura: la interfaz solo sabe si hay una guardada (`has_password`).
- Buzones y dominios del directorio se buscan en el servidor (`?search`, y `?domain` en
  buzones). Los selectores de dominio (`pages/shared/DirectoryDomainPicker.tsx`) piden la
  pagina maxima que publica `GET /mail-directory/meta` y avisan si hay mas coincidencias; sin
  permiso para leer el directorio, el dominio se escribe a mano.
- El historial del doble opt-in no muestra ni pide el enlace de confirmacion: es una
  credencial y el servicio no lo guarda.
