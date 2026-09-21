# Calentamiento de la IP de salida del correo corporativo

Alcance: la IP desde la que Postfix (`deploy/mail/postfix`) entrega el correo corporativo. El
correo transaccional y el de marketing salen por Amazon SES con su propia reputación y no se calientan
aquí (`Plan_Estrategico_Mejoras_Correo.md`, A2; corporativo y marketing nunca comparten reputación).

Estado: procedimiento escrito, sin ejecutar. Lo ejecuta quien opera el servidor. Antes de empezar, la
salida por el puerto 25 tiene que estar confirmada (Netcup la bloquea por defecto) y
`ops/deliverability/verificar-entrega.sh <dominio> --selector <selector>` tiene que dar cero fallas.

## Por qué

Una IP sin historial que envía volumen el primer día se parece a una IP de spam: los grandes
proveedores la difieren o la rechazan y la reputación que se gana en esos días queda marcada. La
reputación se construye enviando poco, a destinatarios que abren y responden, y subiendo de forma
gradual mientras los rebotes y las quejas se mantienen bajos.

## Mecanismo: el límite de envío por dominio que ya existe

`mail-security` administra límites de envío por buzón o por dominio (`RL_VALUE`, valor `N / 1d`,
que aplica Rspamd en `DYN_RL_CHECK`). La rampa es subir ese límite del dominio por etapas; no hace falta
código nuevo. Por API (permisos `rate_limits/{read,update,delete}`), con la sesión de `tenant_admin`
de la empresa o de `superadmin`:

```
PUT    /api/v1/mail-security/rate-limits/{dominio}   {"value": "100 / 1d"}
GET    /api/v1/mail-security/rate-limits/{dominio}
DELETE /api/v1/mail-security/rate-limits/{dominio}   (al terminar la rampa)
```

También está en la pestaña de límites de la ficha de seguridad (`RateLimitsTab`). Un envío que
supera el límite queda registrado en la lista `RL_LOG` de Redis de la celda (`LRANGE RL_LOG 0 20`;
no hay pantalla que la muestre): sirve para saber si la rampa está frenando de más.

## Rampa propuesta

Se pasa a la etapa siguiente solo si la actual cumple las condiciones de la sección siguiente.
Valores por dominio, de partida; ajustar al volumen real de la empresa.

| Etapa | Duración mínima | Límite del dominio |
|---|---|---|
| 1 | 3 días | `50 / 1d` |
| 2 | 3 días | `150 / 1d` |
| 3 | 4 días | `500 / 1d` |
| 4 | 7 días | `1500 / 1d` |
| 5 | 7 días | `5000 / 1d` |
| 6 | 7 días | `15000 / 1d` |
| Fin | | quitar el límite (o dejar el que el plan de la empresa fije) |

Unas cuatro a seis semanas en total. Con empresas de poco volumen se queda en la etapa que cubra su
uso real y no hace falta seguir.

## Reglas durante la rampa

* Empezar por destinatarios conocidos y que respondan: equipos internos, clientes activos. No enviar
  listas frías por esta IP (para eso está el plano de marketing).
* No subir de etapa si en la actual hubo: rebotes duros por encima del 2 % de lo enviado, un solo
  aviso de queja en el bucle de retroalimentación o en Postmaster Tools, la IP en una lista negra
  (`verificar-entrega.sh` la comprueba) o diferimientos masivos de un mismo proveedor (`421`/`451`
  con texto de reputación en el registro de Postfix).
* Si aparece cualquiera de esos avisos: volver a la etapa anterior y esperar tres días, no seguir
  empujando.
* Mantener el mismo remitente y los mismos dominios durante la rampa: cambiarlos reinicia la
  reputación del dominio.

## Qué mirar y dónde

| Señal | Dónde |
|---|---|
| Rebotes y diferidos por proveedor | Registro de Postfix (`status=bounced`, `status=deferred`) |
| Envíos frenados por el límite | Lista `RL_LOG` en Redis de la celda |
| Reputación de la IP y del dominio | Google Postmaster Tools y Microsoft SNDS (hay que registrarlos, P) |
| Listas negras y autenticación | `ops/deliverability/verificar-entrega.sh` |
| Informes de DMARC | Buzón que recibe los `rua` (A3 del plan) |

## Lo que este procedimiento no cubre

Postmaster Tools y SNDS requieren dar de alta el dominio y la IP con la cuenta de quien opera el
servidor; no se automatizan aquí. Tampoco cubre el cambio de IP: si se migra de servidor
(por ejemplo al pasar a un servidor de más memoria), la IP nueva empieza la rampa desde la etapa 1.
