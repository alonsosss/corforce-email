# Ajuste del antispam con datos

Regla de esta plataforma (`Plan_Estrategico_Mejoras_Correo.md`, A4): ningun peso de Rspamd se cambia sin un dato
que lo justifique, y cada cambio se registra con su motivo y con la medida que lo movio.

## Que se mide

`mail-security` cuenta la cuarentena de la celda en `mail_security_quarantine_messages_total{outcome}`:

| `outcome` | Significa |
|---|---|
| `stored` | Mensajes retenidos por el antispam |
| `released` | Retenidos que su dueno libero (por la pantalla o por el enlace del aviso): los **falsos positivos** confirmados por una persona |
| `discarded` | Retenidos que se descartaron (por la pantalla o por el enlace) |
| `learned_spam` | Retenidos que alguien uso para entrenar el clasificador como spam: los **verdaderos positivos** confirmados |
| `learned_ham` | Liberados con "Liberar y marcar como legitimo": ademas de entregarse, ensenaron al clasificador que ese correo es bueno |

Son totales de la celda, sin etiqueta de empresa ni de buzon: una etiqueta asi no acota su cardinalidad. Las
series nacen a cero, de modo que `increase()` ve la primera.

Consultas de partida (ajustar la ventana al volumen; con poco trafico, 30 dias):

```
# Tasa de falsos positivos confirmados de la semana: liberados sobre retenidos
sum(increase(mail_security_quarantine_messages_total{outcome="released"}[7d]))
  / sum(increase(mail_security_quarantine_messages_total{outcome="stored"}[7d]))

# Cuanto de lo revisado resulto spam: entrenados sobre (entrenados + liberados)
sum(increase(mail_security_quarantine_messages_total{outcome="learned_spam"}[7d]))
  / sum(increase(mail_security_quarantine_messages_total{outcome=~"learned_spam|released"}[7d]))
```

Limite honesto: solo cuenta lo que alguien revisa. Un mensaje retenido que nadie abre no es ni falso ni verdadero
positivo; la cuarentena caduca sola (`max_age_days`). La tasa es una cota, no la verdad; con mucha cuarentena sin
revisar, no la use para bajar umbrales.

## Cuando se puede tocar un peso

1. Hay datos de al menos dos semanas de trafico real (sin ellos, no se cambia nada: los pesos por defecto de
   Rspamd estan calibrados con mucho mas correo del que tendra esta celda al principio).
2. La medida apunta a un problema concreto: falsos positivos altos con un simbolo dominante en los liberados
   (los simbolos de cada mensaje van en `symbols` de la cuarentena), o spam que llega al buzon con un simbolo comun.
3. El cambio es el minimo que corrige ese caso: un umbral por empresa (`PUT /api/v1/mail-security/spam-scores/{objeto}`)
   antes que un peso global.

## Registro de cambios

Una fila por cambio, en el mismo commit o tarea que lo hace; se rellena la medida de despues a las dos semanas.

| Fecha | Objeto | Antes | Despues | Motivo y dato | Medida antes | Medida despues |
|---|---|---|---|---|---|---|
| (sin cambios: la celda aun no tiene trafico real) | | | | | | |

## Aprendizaje que ya existe

Lo que el usuario marca como spam desde la cuarentena entrena el clasificador (`POST .../quarantine/{id}/learn-spam`,
contador `learned_spam`). El camino contrario es "Liberar y marcar como legitimo"
(`POST .../quarantine/{id}/release-ham`, contador `learned_ham`): libera el mensaje y despues lo usa para
entrenar Rspamd como legitimo. Exige el permiso de liberar y el de entrenar, va aparte de liberar a secas (que no
entrena: quien libera por prisa un spam no debe envenenar el clasificador) y, si el controller de Rspamd no
responde, el mensaje queda liberado igualmente y el fallo solo se registra.

## DQS de Spamhaus

`SPAMHAUS_DQS_KEY` ya esta prevista en el compose de los motores. Con volumen real, los resolvedores publicos
limitan las consultas y Spamhaus las bloquea. Antes de activarla hay que comprobar en las condiciones de
Spamhaus si el uso previsto (correo de empresas clientes, es decir comercial) exige una suscripcion de pago, y
guardar la clave solo en el almacen de secretos. No se activa por defecto ni sin esa comprobacion.
