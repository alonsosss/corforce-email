# Temporizador de aviso de lo pendiente de desplegar

`scripts/aviso-pendiente.sh` lee el estado de produccion (solo lectura, por SSH) y mantiene una
incidencia `pendiente-despliegue` mientras `main` tenga algo que produccion no. Corre en la maquina de
quien despliega porque es la unica con la clave del servidor; GitHub no la tiene. Si esa maquina esta
apagada el lunes, `Persistent=true` lo ejecuta al encenderse.

Instalacion (unidad de usuario, sin sudo; con `loginctl enable-linger` corre aunque no haya sesion):

```
mkdir -p ~/.config/systemd/user
cp ops/maintenance/systemd/core-force-mail-aviso-pendiente.* ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now core-force-mail-aviso-pendiente.timer
```

Si el clon no esta en `~/Escritorio/correo Email`, editar `WorkingDirectory` y `ExecStart` antes de copiar.
Prueba sin tocar la incidencia: `AVISO_SIN_INCIDENCIA=1 scripts/aviso-pendiente.sh`.
Registro: `journalctl --user -u core-force-mail-aviso-pendiente.service`.
