#!/usr/bin/env python3
"""Cliente minimo de OpenBao para el almacen de secretos (docs/adr/0011).

Lo usan store.sh (leer y escribir el documento de secretos), ops/security/openbao/instalar.sh
(inicializar y configurar) y el respaldo (instantanea de raft). Solo biblioteca estandar: el
servidor ya tiene python3 y no se instala nada mas; tampoco el CLI de OpenBao, que habria que
mantener a la par de la imagen.

Ningun token ni credencial pasa por la linea de comandos ni por el entorno: el role_id y el
secret_id se leen de ficheros 0600 del usuario que ejecuta, el token de sesion vive solo en la
memoria de este proceso y se revoca al terminar. Un argumento quedaria en `ps` para cualquier
usuario del host.

Uso:
  openbao.py salud
  openbao.py leer [--permitir-vacio]          # JSON del documento por stdout (credencial despliegue)
  openbao.py escribir                          # JSON por stdin (credencial administracion)
  openbao.py versiones                         # numero, fecha y estado de cada version
  openbao.py volver-a-version N                # publica como nueva la version N
  openbao.py instantanea FICHERO               # instantanea de raft (credencial respaldo)
  openbao.py inicializar DIR_POLITICAS         # primer arranque: init, configuracion, revoca root
  openbao.py configurar DIR_POLITICAS          # reaplica la configuracion (credencial administracion)
  openbao.py verificar-instantanea FICHERO    # restaura en una instancia desechable y lee

Variables:
  OPENBAO_ADDR       por defecto http://127.0.0.1:8200. Solo se admite loopback: el listener no
                     tiene TLS porque nunca sale de la maquina (ops/security/openbao/config.hcl.tmpl).
  OPENBAO_CRED_DIR   ficheros <rol>.role_id y <rol>.secret_id y la clave de recuperacion (por
                     defecto /opt/core-force-mail/secrets/openbao).
"""

import json
import os
import stat
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ADDR = os.environ.get("OPENBAO_ADDR", "http://127.0.0.1:8200").rstrip("/")
CRED_DIR = os.environ.get("OPENBAO_CRED_DIR", "/opt/core-force-mail/secrets/openbao")
MONTAJE = "cf"
DOCUMENTO = "plataforma"
VERSIONES_CONSERVADAS = 30
ROLES = {
    # rol: (politica, ttl del token). Tokens cortos: cada uso inicia sesion y revoca al salir, el
    # ttl solo acota lo que vive un token si el proceso muere antes de revocarlo.
    "despliegue": ("cf-despliegue", "5m"),
    "administracion": ("cf-administracion", "15m"),
    "respaldo": ("cf-respaldo", "10m"),
}


class Fallo(Exception):
    pass


def _exigir_loopback():
    host = urllib.parse.urlparse(ADDR).hostname
    if host not in ("127.0.0.1", "localhost", "::1"):
        raise Fallo(f"OPENBAO_ADDR debe ser loopback (es {host}): el listener no lleva TLS")


def _peticion(metodo, ruta, token=None, cuerpo=None, crudo=False, esperar=(200, 204)):
    datos = None if cuerpo is None else json.dumps(cuerpo).encode()
    req = urllib.request.Request(ADDR + "/v1/" + ruta, data=datos, method=metodo)
    if token:
        req.add_header("X-Vault-Token", token)
    if datos is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            contenido = resp.read()
            codigo = resp.status
    except urllib.error.HTTPError as e:
        contenido, codigo = e.read(), e.code
    except urllib.error.URLError as e:
        raise Fallo(f"OpenBao no responde en {ADDR} ({e.reason})") from None
    if codigo not in esperar:
        try:
            errores = "; ".join(json.loads(contenido).get("errors", [])) or str(codigo)
        except ValueError:
            errores = str(codigo)
        raise _FalloHTTP(codigo, f"{metodo} {ruta}: {errores}")
    if crudo:
        return contenido
    return json.loads(contenido) if contenido else {}


class _FalloHTTP(Fallo):
    def __init__(self, codigo, mensaje):
        super().__init__(mensaje)
        self.codigo = codigo


def _leer_privado(ruta):
    """Lee un fichero que solo puede ser del usuario que ejecuta y 0600."""
    try:
        st = os.stat(ruta)
    except FileNotFoundError:
        raise Fallo(f"falta {ruta} (corre ops/security/openbao/instalar.sh)") from None
    if st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) & 0o077:
        raise Fallo(f"{ruta} debe ser 0600 del usuario que ejecuta; no se usa")
    with open(ruta, encoding="utf-8") as fh:
        return fh.read().strip()


def _escribir_privado(ruta, contenido):
    os.makedirs(os.path.dirname(ruta), mode=0o700, exist_ok=True)
    tmp = ruta + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(contenido)
    os.replace(tmp, ruta)


class Sesion:
    """Token de un rol de AppRole, revocado al salir del bloque."""

    def __init__(self, rol):
        self.rol = rol
        self.token = None

    def __enter__(self):
        _exigir_loopback()
        role_id = _leer_privado(os.path.join(CRED_DIR, f"{self.rol}.role_id"))
        secret_id = _leer_privado(os.path.join(CRED_DIR, f"{self.rol}.secret_id"))
        try:
            r = _peticion("POST", "auth/approle/login", cuerpo={"role_id": role_id, "secret_id": secret_id})
        except _FalloHTTP as e:
            if e.codigo in (400, 403):
                raise Fallo(f"OpenBao rechazo la credencial {self.rol} (revisa {CRED_DIR})") from None
            if e.codigo == 503:
                raise Fallo("OpenBao esta sellado: no pudo leer su llave de desbloqueo") from None
            raise
        self.token = r["auth"]["client_token"]
        return self

    def __exit__(self, *exc):
        if self.token:
            try:
                _peticion("POST", "auth/token/revoke-self", token=self.token)
            except Fallo:
                pass
        return False


def salud():
    _exigir_loopback()
    try:
        r = _peticion("GET", "sys/health", esperar=(200, 429, 472, 473, 501, 503))
    except Fallo as e:
        print(f"openbao: {e}", file=sys.stderr)
        return 1
    estado = "sin inicializar" if not r.get("initialized") else ("sellado" if r.get("sealed") else "operativo")
    print(f"openbao: {estado} (version {r.get('version', '?')})")
    return 0 if estado == "operativo" else 1


def leer(permitir_vacio=False):
    with Sesion("despliegue") as s:
        try:
            r = _peticion("GET", f"{MONTAJE}/data/{DOCUMENTO}", token=s.token)
        except _FalloHTTP as e:
            if e.codigo == 404 and permitir_vacio:
                sys.stdout.write("{}")
                return 0
            if e.codigo == 404:
                raise Fallo("el almacen de OpenBao esta vacio (corre ops/security/openbao/migrar.sh)") from None
            raise
    sys.stdout.write(json.dumps(r["data"]["data"]))
    return 0


def escribir():
    datos = json.load(sys.stdin)
    if not isinstance(datos, dict) or not all(isinstance(v, str) for v in datos.values()):
        raise Fallo("el documento de secretos debe ser un objeto JSON de cadenas")
    with Sesion("administracion") as s:
        r = _peticion("POST", f"{MONTAJE}/data/{DOCUMENTO}", token=s.token, cuerpo={"data": datos})
    print(f"openbao: publicada la version {r['data']['version']}", file=sys.stderr)
    return 0


def versiones():
    with Sesion("administracion") as s:
        r = _peticion("GET", f"{MONTAJE}/metadata/{DOCUMENTO}", token=s.token)
    meta = r["data"]
    for n, v in sorted(meta["versions"].items(), key=lambda kv: int(kv[0])):
        estado = "destruida" if v.get("destroyed") else ("borrada" if v.get("deletion_time") else "vigente")
        marca = " (actual)" if int(n) == meta["current_version"] else ""
        print(f"{n}\t{v['created_time']}\t{estado}{marca}")
    return 0


def volver_a_version(numero):
    with Sesion("administracion") as s:
        r = _peticion("GET", f"{MONTAJE}/data/{DOCUMENTO}?version={int(numero)}", token=s.token)
        datos = r["data"]["data"]
        if not datos:
            raise Fallo(f"la version {numero} no tiene contenido (borrada o destruida)")
        nueva = _peticion("POST", f"{MONTAJE}/data/{DOCUMENTO}", token=s.token, cuerpo={"data": datos})
    print(f"openbao: la version {numero} se publico como version {nueva['data']['version']}")
    return 0


def instantanea(destino):
    with Sesion("respaldo") as s:
        contenido = _peticion("GET", "sys/storage/raft/snapshot", token=s.token, crudo=True)
    if len(contenido) < 1024:
        raise Fallo("la instantanea de OpenBao llego vacia o truncada")
    fd = os.open(destino, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as fh:
        fh.write(contenido)
    print(f"openbao: instantanea de {len(contenido)} bytes en {destino}", file=sys.stderr)
    return 0


def _configurar(token, dir_politicas):
    """Idempotente: deja montaje, politicas y roles como dice el repositorio. La auditoria es
    declarativa (config.hcl.tmpl)."""
    montajes = _peticion("GET", "sys/mounts", token=token)["data"]
    if f"{MONTAJE}/" not in montajes:
        _peticion("POST", f"sys/mounts/{MONTAJE}", token=token,
                  cuerpo={"type": "kv", "options": {"version": "2"}, "description": "Core Force Mail"})
    # Un montaje kv v2 recien creado tarda unos segundos en quedar operativo ("Upgrading from
    # non-versioned to versioned data").
    for intento in range(30):
        try:
            _peticion("POST", f"{MONTAJE}/config", token=token, cuerpo={"max_versions": VERSIONES_CONSERVADAS})
            break
        except _FalloHTTP:
            if intento == 29:
                raise
            time.sleep(1)

    for nombre in sorted(os.listdir(dir_politicas)):
        if nombre.endswith(".hcl"):
            with open(os.path.join(dir_politicas, nombre), encoding="utf-8") as fh:
                _peticion("POST", f"sys/policies/acl/{nombre[:-4]}", token=token, cuerpo={"policy": fh.read()})

    auths = _peticion("GET", "sys/auth", token=token)["data"]
    if "approle/" not in auths:
        _peticion("POST", "sys/auth/approle", token=token, cuerpo={"type": "approle"})

    for rol, (politica, ttl) in ROLES.items():
        _peticion("POST", f"auth/approle/role/cf-{rol}", token=token, cuerpo={
            "token_policies": [politica],
            "token_ttl": ttl,
            "token_max_ttl": ttl,
            "token_type": "service",
            # Solo desde la propia maquina: el listener es loopback, pero atar la credencial a la
            # direccion la deja inservible si alguna vez se expusiera el puerto.
            "secret_id_bound_cidrs": ["127.0.0.1/32"],
            "token_bound_cidrs": ["127.0.0.1/32"],
            "bind_secret_id": True,
        })
        role_id = _peticion("GET", f"auth/approle/role/cf-{rol}/role-id", token=token)["data"]["role_id"]
        _escribir_privado(os.path.join(CRED_DIR, f"{rol}.role_id"), role_id)
        ruta_secret = os.path.join(CRED_DIR, f"{rol}.secret_id")
        if not os.path.exists(ruta_secret):
            secret_id = _peticion("POST", f"auth/approle/role/cf-{rol}/secret-id", token=token)["data"]["secret_id"]
            _escribir_privado(ruta_secret, secret_id)
    print("openbao: configuracion aplicada (montaje, politicas y roles)", file=sys.stderr)


def _esperar_activo(segundos=60):
    """Tras init o un arranque, raft tarda en elegir lider: hasta entonces todo da 5xx."""
    for _ in range(segundos):
        try:
            _peticion("GET", "sys/health")
            return
        except Fallo:
            time.sleep(1)
    raise Fallo(f"OpenBao no quedo activo en {segundos} s")


def inicializar(dir_politicas):
    _exigir_loopback()
    ruta_root = os.path.join(CRED_DIR, "root.token")
    estado = _peticion("GET", "sys/init")
    if estado.get("initialized") and not os.path.exists(ruta_root):
        return configurar(dir_politicas)
    if not estado.get("initialized"):
        r = _peticion("POST", "sys/init", cuerpo={"recovery_shares": 1, "recovery_threshold": 1})
        # La clave de recuperacion no desbloquea (eso lo hace la llave estatica), pero permite
        # generar un token root si se pierden las credenciales de administracion. Se guarda aparte
        # para que quien opera la copie fuera del servidor y la borre de aqui.
        _escribir_privado(os.path.join(CRED_DIR, "recuperacion.key"), r["recovery_keys_base64"][0] + "\n")
        # El root se guarda mientras dura la configuracion: si esta falla a medias, la siguiente
        # corrida la reanuda con el en vez de dejar una instancia inicializada e inoperable.
        _escribir_privado(ruta_root, r["root_token"])
    root = _leer_privado(ruta_root)
    _esperar_activo()
    _configurar(root, dir_politicas)
    # El token root no sobrevive a la instalacion: ninguna tarea del dia a dia lo necesita.
    _peticion("POST", "auth/token/revoke-self", token=root)
    os.remove(ruta_root)
    print("openbao: inicializado; token root revocado", file=sys.stderr)
    return 0


def configurar(dir_politicas):
    _esperar_activo()
    with Sesion("administracion") as s:
        _configurar(s.token, dir_politicas)
    return 0


def verificar_instantanea(origen):
    """Restaura una instantanea en una instancia DESECHABLE y lee el documento con la credencial de
    despliegue que viaja dentro de ella. Es la prueba de que respaldo y llave sirven juntos.

    Se niega si la instancia de OPENBAO_ADDR ya esta inicializada: snapshot-force sobre la de
    produccion la devolveria al pasado."""
    _exigir_loopback()
    if _peticion("GET", "sys/init").get("initialized"):
        raise Fallo(f"{ADDR} ya esta inicializada: la verificacion solo corre contra una instancia vacia")
    root = _peticion("POST", "sys/init", cuerpo={"recovery_shares": 1, "recovery_threshold": 1})["root_token"]
    _esperar_activo()
    with open(origen, "rb") as fh:
        contenido = fh.read()
    req = urllib.request.Request(ADDR + "/v1/sys/storage/raft/snapshot-force", data=contenido, method="POST")
    req.add_header("X-Vault-Token", root)
    try:
        with urllib.request.urlopen(req, timeout=120):
            pass
    except urllib.error.HTTPError as e:
        raise Fallo(f"la instantanea no se pudo restaurar ({e.code}): llave distinta o fichero danado") from None
    _esperar_activo()
    with Sesion("despliegue") as s:
        datos = _peticion("GET", f"{MONTAJE}/data/{DOCUMENTO}", token=s.token)["data"]["data"]
    if not datos:
        raise Fallo("la instantanea restaura pero el documento de secretos esta vacio")
    print(f"openbao: la instantanea restaura y devuelve {len(datos)} secretos")
    return 0


def main(argv):
    if not argv:
        print(__doc__, file=sys.stderr)
        return 2
    orden, resto = argv[0], argv[1:]
    try:
        if orden == "salud":
            return salud()
        if orden == "leer":
            return leer(permitir_vacio="--permitir-vacio" in resto)
        if orden == "escribir":
            return escribir()
        if orden == "versiones":
            return versiones()
        if orden == "volver-a-version" and len(resto) == 1:
            return volver_a_version(resto[0])
        if orden == "instantanea" and len(resto) == 1:
            return instantanea(resto[0])
        if orden == "inicializar" and len(resto) == 1:
            return inicializar(resto[0])
        if orden == "configurar" and len(resto) == 1:
            return configurar(resto[0])
        if orden == "verificar-instantanea" and len(resto) == 1:
            return verificar_instantanea(resto[0])
    except Fallo as e:
        print(f"openbao: {e}", file=sys.stderr)
        return 1
    print(f"openbao: orden desconocida: {' '.join(argv)}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
