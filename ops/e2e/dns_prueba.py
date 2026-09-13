#!/usr/bin/env python3
"""DNS autoritativo minimo de la prueba de punta a punta (ops/e2e/run.sh).

Responde por UDP en 127.0.0.1:<puerto> con los registros TXT y MX de <zona.json>, que es la
lista dns_records que devuelve domain-service al dar de alta un dominio ({host, type, value};
el MX como "<destino> priority <n>"). Lee la zona en cada consulta: la prueba la publica
escribiendo el fichero. Un nombre que no esta en la zona responde NXDOMAIN y uno que esta sin
ese tipo, una respuesta vacia. Solo biblioteca estandar: nada que instalar.

Uso: python3 dns_prueba.py <puerto> <zona.json>
"""
import json
import socket
import struct
import sys

TIPOS = {"TXT": 16, "MX": 15}
# QR, AA y RA: respuesta autoritativa; RD se copia de la consulta.
BANDERAS = 0x8480


def leer_nombre(datos, i):
    etiquetas = []
    while datos[i]:
        n = datos[i]
        etiquetas.append(datos[i + 1:i + 1 + n].decode("ascii").lower())
        i += 1 + n
    return ".".join(etiquetas), i + 1


def codificar_nombre(texto):
    salida = b""
    for etiqueta in texto.rstrip(".").split("."):
        b = etiqueta.encode("ascii")
        salida += bytes([len(b)]) + b
    return salida + b"\0"


def rdata(tipo, valor):
    if tipo == "MX":
        destino, _, prioridad = valor.split()
        return struct.pack("!H", int(prioridad)) + codificar_nombre(destino)
    b = valor.encode()
    return b"".join(bytes([len(b[i:i + 255])]) + b[i:i + 255] for i in range(0, len(b), 255))


def leer_zona(ruta):
    try:
        with open(ruta) as f:
            return json.load(f)
    except (OSError, ValueError):
        return []


def responder(consulta, registros):
    ident, banderas = struct.unpack("!HH", consulta[:4])
    qname, fin = leer_nombre(consulta, 12)
    qtype = struct.unpack("!H", consulta[fin:fin + 2])[0]
    pregunta = consulta[12:fin + 4]
    del_nombre = [r for r in registros if r["host"].rstrip(".").lower() == qname]
    respuestas = [r for r in del_nombre if TIPOS.get(r["type"]) == qtype]
    rcode = 0 if del_nombre else 3
    salida = struct.pack("!HHHHHH", ident, BANDERAS | (banderas & 0x0100) | rcode, 1, len(respuestas), 0, 0) + pregunta
    for r in respuestas:
        d = rdata(r["type"], r["value"])
        salida += b"\xc0\x0c" + struct.pack("!HHIH", qtype, 1, 60, len(d)) + d
    return salida


def main():
    puerto, ruta = int(sys.argv[1]), sys.argv[2]
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", puerto))
    while True:
        consulta, origen = sock.recvfrom(4096)
        try:
            sock.sendto(responder(consulta, leer_zona(ruta)), origen)
        except (IndexError, KeyError, ValueError, struct.error) as e:
            print(f"consulta no valida de {origen}: {e}", file=sys.stderr, flush=True)


if __name__ == "__main__":
    main()
