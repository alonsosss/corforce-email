#!/usr/bin/env python3
"""Cloudflare falso de la prueba de punta a punta (ops/e2e/run.sh).

Sirve por HTTP en 127.0.0.1:<puerto> la parte de la API v4 que usa domain-service
(services/domain-service/internal/adapters/cloudflare): GET /client/v4/user/tokens/verify,
GET /client/v4/zones paginado, y GET, POST, PUT y DELETE de /client/v4/zones/<id>/dns_records.
Responde con los codigos de Cloudflare: 401 con 1000 a un token que no conoce, 404 con 7003 a una
zona que el token no ve, 400 con 81057 a un registro identico.

<config.json> declara los tokens con su estado, sus zonas y si les falta permiso (403 con 9109), los registros que el cliente ya tiene
y cuantas zonas de relleno ve cada token activo (para recorrer varias paginas). Todo registro que
se crea, cambia o borra se escribe en <zona.json>, la zona que sirve ops/e2e/dns_prueba.py, sin
tocar las entradas que la prueba publica a mano: asi la verificacion real de domain-service ve lo
publicado. Las entradas propias llevan "cf_id". GET /__registros?zone=<nombre> devuelve los
registros de una zona sin autenticar, solo para que la prueba los inspeccione. El token nunca se
escribe en el registro de peticiones. Solo biblioteca estandar.

Uso: python3 cloudflare_prueba.py <puerto> <config.json> <zona.json>
"""
import json
import os
import sys
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

PREFIJO = "/client/v4"
cerrojo = threading.Lock()


def nuevo_id():
    return uuid.uuid4().hex


def sin_comillas(valor):
    """Lo que responde el DNS para un TXT guardado con comillas: las cadenas unidas y sin comillas.
    Cloudflare guarda el contenido tal como se escribio y el DNS nunca las devuelve."""
    valor = valor.strip()
    if not valor.startswith('"'):
        return valor
    salida, dentro, escapado = [], False, False
    for c in valor:
        if escapado:
            salida.append(c); escapado = False
        elif dentro and c == "\\":
            escapado = True
        elif c == '"':
            dentro = not dentro
        elif dentro:
            salida.append(c)
    return "".join(salida)


class Estado:
    def __init__(self, config, ruta_zona):
        self.ruta_zona = ruta_zona
        self.tokens = config["tokens"]
        self.relleno = int(config.get("filler_zones", 0))
        nombres = set()
        for t in self.tokens.values():
            nombres.update(t.get("zones", []))
        self.zonas = {n: nuevo_id() for n in sorted(nombres)}
        self.registros = {zid: [] for zid in self.zonas.values()}
        for r in config.get("seed", []):
            self.registros[self.zonas[r["zone"]]].append(self.normalizar(r))
        self.escribir_zona()

    @staticmethod
    def normalizar(r):
        out = {
            "id": nuevo_id(),
            "type": r["type"],
            "name": r["name"].rstrip(".").lower(),
            "content": r["content"],
            "ttl": r.get("ttl", 1),
            "comment": r.get("comment") or "",
        }
        if r["type"] == "MX":
            out["priority"] = int(r.get("priority", 10))
        return out

    def zonas_de(self, token):
        t = self.tokens[token]
        propias = [{"id": self.zonas[n], "name": n, "status": "active"} for n in t.get("zones", [])]
        if t.get("status") == "active":
            propias += [
                {"id": "%032x" % (i + 1), "name": "relleno-%d.test" % i, "status": "active"}
                for i in range(self.relleno)
            ]
        return propias

    def escribir_zona(self):
        try:
            with open(self.ruta_zona) as f:
                zona = json.load(f)
        except (OSError, ValueError):
            zona = []
        zona = [e for e in zona if "cf_id" not in e]
        for registros in self.registros.values():
            for r in registros:
                valor = r["content"]
                if r["type"] == "TXT":
                    valor = sin_comillas(valor)
                if r["type"] == "MX":
                    valor = "%s priority %d" % (r["content"], r["priority"])
                zona.append({"host": r["name"], "type": r["type"], "value": valor, "cf_id": r["id"]})
        tmp = self.ruta_zona + ".cf.tmp"
        with open(tmp, "w") as f:
            json.dump(zona, f)
        os.replace(tmp, self.ruta_zona)


class Manejador(BaseHTTPRequestHandler):
    estado = None

    def log_message(self, formato, *args):
        # El registro no lleva cabeceras: el token viaja en Authorization.
        sys.stderr.write("%s %s\n" % (self.command, self.path))

    def responder(self, status, result=None, errores=None, info=None):
        cuerpo = {
            "success": 200 <= status < 300,
            "errors": [{"code": c, "message": "error %d" % c} for c in (errores or [])],
            "messages": [],
            "result": result,
        }
        if info is not None:
            cuerpo["result_info"] = info
        datos = json.dumps(cuerpo).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(datos)))
        self.end_headers()
        self.wfile.write(datos)

    def cuerpo(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")

    def token(self):
        auth = self.headers.get("Authorization", "")
        tok = auth[len("Bearer "):] if auth.startswith("Bearer ") else ""
        return tok if tok in self.estado.tokens else None

    def atender(self):
        url = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        est = self.estado
        if url.path == "/__registros":
            zid = est.zonas.get(q.get("zone", ""))
            return self.responder(200, est.registros.get(zid, []))
        if not url.path.startswith(PREFIJO):
            return self.responder(404, errores=[7000])
        ruta = url.path[len(PREFIJO):].rstrip("/").split("/")[1:]
        tok = self.token()
        if tok is None:
            return self.responder(401, errores=[1000])
        datos_token = est.tokens[tok]
        if ruta == ["user", "tokens", "verify"] and self.command == "GET":
            return self.responder(200, {"id": "tok", "status": datos_token.get("status", "active")})
        if datos_token.get("status") != "active":
            return self.responder(401, errores=[1000])
        if datos_token.get("forbidden"):
            return self.responder(403, errores=[9109])
        visibles = est.zonas_de(tok)
        if ruta == ["zones"] and self.command == "GET":
            pagina, por = int(q.get("page", 1)), int(q.get("per_page", 20))
            total = max(1, -(-len(visibles) // por))
            trozo = visibles[(pagina - 1) * por:pagina * por]
            info = {"page": pagina, "per_page": por, "count": len(trozo), "total_count": len(visibles), "total_pages": total}
            return self.responder(200, trozo, info=info)
        if len(ruta) < 3 or ruta[0] != "zones" or ruta[2] != "dns_records":
            return self.responder(404, errores=[7000])
        zid = ruta[1]
        if zid not in [z["id"] for z in visibles] or zid not in est.registros:
            return self.responder(404, errores=[7003])
        registros = est.registros[zid]
        if len(ruta) == 3 and self.command == "GET":
            sel = [r for r in registros
                   if (not q.get("type") or r["type"] == q["type"])
                   and (not q.get("name") or r["name"] == q["name"].rstrip(".").lower())]
            return self.responder(200, sel, info={"page": 1, "per_page": 100, "count": len(sel), "total_pages": 1})
        if len(ruta) == 3 and self.command == "POST":
            nuevo = self.estado.normalizar(self.cuerpo())
            if any(r["type"] == nuevo["type"] and r["name"] == nuevo["name"] and r["content"] == nuevo["content"]
                   for r in registros):
                return self.responder(400, errores=[81057])
            registros.append(nuevo)
            est.escribir_zona()
            return self.responder(200, nuevo)
        if len(ruta) == 4:
            actual = next((r for r in registros if r["id"] == ruta[3]), None)
            if actual is None:
                return self.responder(404, errores=[81044])
            if self.command == "PUT":
                cambio = self.estado.normalizar(self.cuerpo())
                cambio["id"] = actual["id"]
                registros[registros.index(actual)] = cambio
                est.escribir_zona()
                return self.responder(200, cambio)
            if self.command == "DELETE":
                registros.remove(actual)
                est.escribir_zona()
                return self.responder(200, {"id": actual["id"]})
        return self.responder(405, errores=[10000])

    def manejar(self):
        with cerrojo:
            try:
                self.atender()
            except (KeyError, ValueError, TypeError) as e:
                sys.stderr.write("peticion no valida: %s\n" % e)
                self.responder(400, errores=[9005])

    do_GET = do_POST = do_PUT = do_DELETE = manejar


def main():
    puerto, ruta_config, ruta_zona = int(sys.argv[1]), sys.argv[2], sys.argv[3]
    with open(ruta_config) as f:
        Manejador.estado = Estado(json.load(f), ruta_zona)
    ThreadingHTTPServer(("127.0.0.1", puerto), Manejador).serve_forever()


if __name__ == "__main__":
    main()
