# ADR 0010: El plan contratado limita los buzones y el espacio de cada empresa

## Estado

Implementado y probado (2026-09-22). Sin efecto hasta que existan planes y una empresa esté suscrita: sin
suscripción no hay límite de plan que aplicar y el directorio se comporta como antes. Los planes en sí (nombres,
precios y cantidades) son **datos comerciales** y no van en ninguna migración: los crea el superadmin por API.

## Contexto

Las cuotas de correo existían en tres niveles del directorio (cuota del buzón, y por dominio: cuota por defecto,
máximo por buzón, total del dominio y máximo de buzones) y Dovecot las impone de verdad. Pero nadie las gobernaba:

* En producción **todas estaban en cero**, que en este sistema significa *ilimitado* (2026-09-22). Un solo buzón
  podía llenar el disco del servidor y dejar sin servicio a las demás empresas.
* Quien las fija es el **administrador de la empresa**, sobre su propio dominio. Nada le impedía asignarse el
  espacio que quisiera: no había ningún techo por encima de él.
* `billing` ya tenía el vocabulario completo —los recursos `mailboxes` y `storage_bytes`, planes con límites duros
  o blandos, suscripciones por empresa y `/internal/billing/entitlements/check`, cuyo comentario decía
  literalmente que lo llamarían los demás servicios «antes de crear un buzón o un dominio»— pero **ningún servicio
  lo llamaba** y no había ningún plan creado.

Es decir, la pieza comercial y la pieza operativa existían sin tocarse. Lo que se pedía es el modelo de cualquier
proveedor grande de correo: el **proveedor** decide cuánto espacio incluye cada plan, y el cliente reparte ese
espacio entre sus buzones sin poder excederlo.

## Decisión

**El plan manda sobre el dominio, y el dominio reparte dentro del plan.** `mail-directory` consulta a `billing` lo
que el plan de la empresa incluye y lo aplica en los dos únicos sitios donde se decide:

* al **crear un buzón**, contra `mailboxes`;
* al **fijar o subir una cuota** (alta y edición pasan las dos por `checkQuota`), contra `storage_bytes`.

Cuatro decisiones que no son evidentes:

1. **El límite se cuenta por EMPRESA, no por dominio.** El plan es de la empresa, y una empresa vive entera en una
   celda, así que `mail-directory` puede sumar todos sus buzones y todas sus cuotas con una consulta local. Dos
   dominios de la misma empresa no dan buzones de más.
2. **`billing` da el límite; `mail-directory` pone el consumo.** Se añade `GET /internal/billing/plan-limits`, que
   devuelve solo lo que el plan incluye, en vez de usar `entitlements/check`. Motivo: `billing` cuenta buzones y
   dominios por eventos, pero **no cuenta el espacio asignado**, que solo conoce el dueño del directorio. Usar el
   veredicto de `check` para el espacio habría comparado contra un consumo que siempre vale cero, y hacer que
   `billing` lo contara habría exigido meter la cuota en el contrato de los eventos de buzón, con el riesgo de
   deriva y una reconciliación para los buzones ya existentes. Cada servicio aporta lo que de verdad sabe.
3. **Se falla hacia el lado abierto.** Si `billing` no responde, o la empresa no tiene plan, o el plan no fija ese
   recurso, no se restringe: se registra el aviso y el alta sigue, con los límites del dominio aplicándose igual.
   Un `billing` caído no puede dejar a una empresa sin poder dar de alta un buzón. Es el mismo criterio que
   `reputation` con su derecho mensual, y la exposición es acotada y visible (queda en el registro).
4. **Un límite blando no bloquea.** En `billing`, un límite blando es el que se puede exceder facturando el exceso;
   solo el duro rechaza. El espacio ilimitado (cuota 0) no cabe en un plan con espacio acotado, igual que no cabe
   en un dominio con cuota acotada.

El rechazo lleva código propio, `PLAN_MAILBOXES_EXCEEDED` y `PLAN_STORAGE_EXCEEDED`, con 409: lo que hay que hacer
es cambiar de plan, no corregir la entrada ni reintentar, y la interfaz tiene que poder decirlo con esas palabras.

## Consecuencias

* El superadmin gobierna el espacio de cada empresa **cambiando su plan**, sin entrar en los datos de la empresa.
  Esto es deliberado: el aislamiento entre empresas no se toca (ver "Lo que no se decide aquí").
* `mail-directory`, servicio de celda, pasa a llamar a `billing`, servicio de plataforma. Es una dependencia
  **opcional**: sin `BILLING_URL` arranca igual y lo avisa. No añade ningún secreto nuevo (reusa
  `INTERNAL_GATEWAY_TOKEN`, que ya recibe).
* Dos consultas locales más por alta de buzón (contar buzones y sumar cuotas de la empresa) y una llamada HTTP de
  2 s de plazo máximo, un intento y cortacircuitos.
* Mientras no haya planes creados, nada cambia: es un despliegue seguro.

## Lo que no se decide aquí

**Que el superadmin vea o edite los dominios y buzones de una empresa cliente.** Hoy no puede: la empresa sale de
la sesión y los módulos de empresa no aceptan una empresa destino. Eso es deliberado y abrirlo es un cambio del
gateway con implicaciones de RBAC y auditoría (las tres capas, permisos con alcance de plataforma en
`access_control`, y `docs/Usuarios_Roles_y_Acceso.md` al día en la misma tarea). Si se necesita, va en su propio
ADR y con una ruta de plataforma explícita, nunca como efecto colateral de las cuotas.
