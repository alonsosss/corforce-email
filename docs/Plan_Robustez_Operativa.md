# Plan: lo que falta para poder dormir tranquilo

Sale de la revisión del 2026-09-25, tras un día en que tres fallos reales aparecieron **usando**
producción y no en las pruebas. No añade funciones: cierra los huecos que las funciones han ido
dejando. En orden de riesgo.

## 1. Saber volver: ensayo de recuperación completa

**El hueco.** Los respaldos ya salen del servidor cifrados y `verify-restore.sh` comprueba
semanalmente que un volcado restaura y trae sus esquemas. Pero eso prueba **una base**, en el mismo
servidor, desde la copia local. Lo que nunca se ha hecho es lo que hará falta el día malo: levantar
la plataforma **en una máquina limpia** partiendo solo de lo que hay fuera del servidor.

Y lo que hay fuera son cuatro cosas, ni una más:

| Qué | Dónde |
|---|---|
| Los volcados de las bases y la instantánea de OpenBao | S3, cifrados |
| Los volúmenes de correo (buzones y sus claves) | S3, cifrados |
| La frase de cifrado de los respaldos | Fuera del servidor |
| La llave de desbloqueo de OpenBao **y su identificador** | Fuera del servidor |
| La credencial de despliegue del almacén (`despliegue.role_id` y `.secret_id`) | Fuera del servidor |
| La **configuración** del servidor (los dos `.env`) | S3, cifrada |

Si con eso no se vuelve, el respaldo no vale. Y eso no se sabe hasta intentarlo.

**Las dos últimas filas las descubrió el propio ensayo.** Antes de correrlo se daba por hecho que
bastaban la frase y la llave. No bastan:

* el sello estático necesita **el identificador** de la llave, no solo la llave;
* y, una vez restaurada la instantánea, el token raíz anterior **deja de valer** (la instantánea trae
  su propio almacén de tokens) y `sys/generate-root` responde error con este sello, así que la clave
  de recuperación **no abre nada**. Lo único que abre es la credencial de AppRole de despliegue, que
  hasta ahora solo existía dentro del servidor. Es decir: **si el servidor hubiera ardido ayer, los
  secretos del respaldo no se habrían podido leer.**

Y una cuarta, encontrada al seguir tirando del hilo: **la configuración del servidor no se
respaldaba**. Los dos `.env` (plataforma y motores) no los versiona nadie, porque describen *este*
servidor: qué celda es, qué dominio sirve, a dónde apunta cada servicio. Con los datos de vuelta
pero sin ellos, habría que reconstruirlos a mano y de memoria. Ahora viajan cifrados en la misma
corrida (`config/<sello>.tar.gz.gpg`) y el ensayo los exige.

Y una quinta, al probarlo de verdad: **la politica de IAM del usuario de respaldos no permitia el
prefijo `config/`**, asi que la configuracion se empaquetaba y se quedaba en el servidor con un
aviso. Un permiso que falta no da error al aprovisionar: falla el dia del respaldo. Corregido y
aplicado en AWS el 2026-09-25.

**Qué se hace.** Un guion de ensayo, `ops/backup/ensayo-recuperacion.sh`, que corre en contenedores
desechables (como `make e2e`) y, **sin tocar producción ni leer nada del servidor**, parte del bucket
y de las dos claves y comprueba por fases:

1. **Descarga y descifrado.** Los volcados de registro, celda y empresa, la instantánea de OpenBao y
   los volúmenes de correo salen del bucket y se descifran con la frase.
2. **Bases.** Se restauran en un Postgres limpio y se comprueban esquemas, migraciones y datos, como
   ya hace `verify-restore.sh`.
3. **Secretos.** OpenBao arranca vacío, se restaura la instantánea, se desbloquea con la llave y se
   lee una clave conocida. Sin esto, las credenciales cifradas de la base no se pueden descifrar y el
   resto no sirve de nada.
4. **Roles de la base.** Se recrean desde el almacén (`ops/db/tenant-service-role.sh` y compañía): no
   viajan en el volcado.
5. **Correo.** El volumen de buzones se restaura y Dovecot arranca contra la base restaurada: un
   buzón conocido autentica por IMAP y sus mensajes están.
6. **Identidad.** El administrador de la plataforma inicia sesión contra el registro restaurado.

Al final imprime **cuánto tardó cada fase**, que es el dato que hoy no tenemos: cuánto dura un
desastre.

**Criterio de cierre.** El ensayo pasa entero, queda en `make` y se corre cada mes; su resultado y su
duración van a `docs/Operacion_Despliegue.md`.

**Resultado (2026-09-25): OK en 48 s**, ya con la configuracion del servidor dentro, partiendo solo del bucket y de lo de fuera, con
5 bases restauradas, 45 secretos leídos y 5 buzones comprobados contra su directorio. Cinco fallos
encontrados y arreglados por el camino: el identificador de la llave y la credencial de despliegue
que faltaban fuera, y un lector de configuración que se comía el relleno final de la llave en base64
(OpenBao arrancaba y fallaba mucho después, que es la peor forma de fallar).

## 2. Nada se despliega con la CI en rojo

**El hueco.** Hoy `main` estuvo en rojo tres commits sin que nadie lo viera, y se desplegó a
producción igualmente. Lo que evitó el problema fue la atención de una persona, no el sistema.

**Qué se hace.** Una guardia más en `scripts/lib/despliegue.sh`, al lado de las que ya existen (árbol
sucio, retroceso de versión): antes de desplegar, se comprueba que el commit tiene la CI en verde en
GitHub. Si está en rojo, en curso o no se encuentra, el despliegue **se detiene** y dice por qué.

Como las otras guardias, con salida explícita: `DEPLOY_CI=avisar` sigue avisando y `DEPLOY_CI=omitir`
no comprueba nada; las dos lo dejan dicho en la salida del despliegue.

Distingue tres casos porque se arreglan de forma distinta: la CI **falla** (corregir), **todavía
corre** (esperar unos minutos) y **no hay ninguna** (falta empujar el commit).

**Criterio de cierre.** Probada con esos tres casos, con el verde de otro commit, con `gh` caído y
con los tres modos, en `ops/scaffold/check-deploy-mail.sh`, y con dos mutaciones que la rompen.

## 3. Rotar lo que quedó expuesto

**El hueco.** En la conversación de hoy se vieron, en capturas o en texto: el token de Cloudflare de
Campovivo y las dos contraseñas de su administrador y su buzón. Están en el historial del chat, que
se comparte.

**Qué se hace.**

| Secreto | Quién |
|---|---|
| Contraseña del administrador de Campovivo | La plataforma, por API |
| Contraseña del buzón `admin@campovivoalimentos.com` | La plataforma, por API |
| Token de Cloudflare de Campovivo | Quien opera: crear uno nuevo acotado a esa zona y reconectarlo |

Las dos primeras se rotan solas y la nueva no pasa por ningún chat: queda en el fichero de
configuración del equipo, con permisos 0600.

**Criterio de cierre.** Las contraseñas viejas dejan de servir, comprobado; el token viejo, borrado
en Cloudflare.

## 4. La integración del ERP, aparcada de forma explícita

**El hueco.** Está a medias. La base (las dos familias de credencial) está hecha y probada, pero no
hay ninguna ruta de aprovisionamiento, así que **no cambia el comportamiento de nada**. Lo peligroso
de las cosas a medias no es que estén a medias, es que nadie recuerde en qué punto quedaron.

**Qué se hace.** Dejarlo escrito en `docs/Plan_Integracion_ERP.md`: qué está hecho, qué falta y por
dónde se retoma. Nada más: no se empieza la fase B2 hasta cerrar los puntos 1 a 3.

**Criterio de cierre.** El estado del plan dice «aparcado» con la fecha y el motivo.

## 5. Auditoria del 2026-09-25

Con todo lo anterior escrito, una revision adversaria del dia encontro cinco cosas reales, corregidas
en `545a715`. Se dejan aqui porque el patron importa mas que los fallos:

* **Un buzon `active=2` («solo recibe») se reenviaba al proveedor anterior.** Es el mismo fallo que
  `b359a0d`, con otro estado, y justo el que se usa al cortar el acceso a alguien durante una
  migracion. El arreglo anterior no miro los otros valores de la columna.
* **El gateway daba por buena una credencial sin familia**, que casaba con cualquiera de las dos. La
  unica capa que separa los poderes estaba escrita a prueba de fallos al reves.
* **La disyuncion de las dos listas de rutas se comprobaba por texto**, no por URL: dos patrones
  distintos que casan la misma direccion pasaban la validacion.
* **La puerta de la CI podia bloquear un rollback de emergencia** y culpaba en falso de no haber
  empujado el commit.
* **Los documentos rectores** seguian describiendo una sola familia de credencial.

Lo que se aprende: cada arreglo deja un borde sin mirar, y las guardias nuevas hay que probarlas
tambien en el dia malo, no solo en el bueno.

## 6. Estado

| Punto | Estado |
|---|---|
| 1. Ensayo de recuperación | Hecho y cerrado: `make ensayo-recuperacion`, OK en 48 s con la configuración incluida, y cinco huecos encontrados y cerrados |
| 2. Guardia de CI en verde | Hecho: `despliegue_comprobar_ci` en los dos despliegues, con sus cuatro casos probados y dos mutaciones |
| 3. Rotación de lo expuesto | Hecho en lo que depende de la plataforma: las dos contraseñas de Campovivo rotadas y comprobadas (la vieja ya no entra, ni por web ni por IMAP). Falta el token de Cloudflare, que lo rota quien opera |
| 4. Integración del ERP aparcada | Hecho: `docs/Plan_Integracion_ERP.md` dice desde dónde se retoma |
