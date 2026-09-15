#!/usr/bin/env python3
"""Cliente IMAP y SMTP de ops/e2e/mail.sh.

Corre dentro de la red de los motores (o dentro del contenedor de Dovecot) y verifica los
certificados con la CA de la prueba contra el nombre del servidor de correo, como un cliente
real. Una linea de resultado por orden: OK ..., NO ... o RECHAZO <fase> <codigo> <texto>;
`buscar` anade despues las cabeceras del primer mensaje encontrado.
"""
import argparse
import base64
import email.utils
import http.client
import imaplib
import smtplib
import socket
import ssl
import sys
import time
from email.message import EmailMessage

# Fichero de prueba estandar de EICAR, partido para que el propio fuente no sea la firma.
EICAR = "X5O!P%@AP[4\\PZX54(P^)7CC)7}$" + "EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"


class IMAPTLS(imaplib.IMAP4_SSL):
    """IMAP con TLS implicito que conecta a una direccion y verifica el nombre del certificado."""

    def __init__(self, host, port, contexto, nombre):
        self._nombre = nombre
        super().__init__(host, port, ssl_context=contexto, timeout=30)

    def _create_socket(self, timeout):
        sock = imaplib.IMAP4._create_socket(self, timeout)
        return self.ssl_context.wrap_socket(sock, server_hostname=self._nombre)


class SMTPTLS(smtplib.SMTP):
    """SMTP cuyo STARTTLS verifica el nombre del certificado y no la direccion conectada."""

    def __init__(self, host, port, nombre):
        super().__init__(host, port, local_hostname="cliente.e2e.test", timeout=60)
        self._host = nombre


def contexto(args):
    return ssl.create_default_context(cafile=args.ca)


def imap(args):
    return IMAPTLS(args.imap_host, args.imap_puerto, contexto(args), args.nombre)


def usuario(args):
    return f"{args.usuario}*{args.maestro}" if args.maestro else args.usuario


def orden_login(args):
    try:
        c = imap(args)
        c.login(usuario(args), args.clave)
        estado, _ = c.select("INBOX", readonly=True)
        c.logout()
        print("OK" if estado == "OK" else f"NO select {estado}")
    except imaplib.IMAP4.error as e:
        print(f"NO {e}")


def contar(c, carpeta, token):
    estado, _ = c.select(carpeta, readonly=True)
    if estado != "OK":
        return []
    estado, datos = c.search(None, "SUBJECT", f'"{token}"')
    return datos[0].split() if estado == "OK" and datos and datos[0] else []


def orden_buscar(args):
    try:
        c = imap(args)
        c.login(usuario(args), args.clave)
    except imaplib.IMAP4.error as e:
        print(f"NO {e}")
        return
    limite = time.time() + args.espera
    ids = contar(c, args.carpeta, args.token)
    while not ids and time.time() < limite:
        time.sleep(1)
        ids = contar(c, args.carpeta, args.token)
    if ids and args.estable:
        time.sleep(args.estable)
        ids = contar(c, args.carpeta, args.token)
    if not ids:
        print("NO 0")
        c.logout()
        return
    _, datos = c.fetch(ids[0], "(BODY.PEEK[HEADER])")
    cabeceras = datos[0][1].decode("utf-8", "replace")
    c.logout()
    print(f"OK {len(ids)}")
    print(cabeceras)


def orden_sesion(args):
    """Abre una sesion IMAP y la mantiene con NOOP: LISTA al entrar, CERRADA si el servidor la
    corta y ABIERTA si sigue viva al cumplir el limite."""
    try:
        c = imap(args)
        c.login(usuario(args), args.clave)
        c.select("INBOX", readonly=True)
    except imaplib.IMAP4.error as e:
        print(f"NO {e}", flush=True)
        return
    print("LISTA", flush=True)
    inicio = time.time()
    while time.time() - inicio < args.limite:
        time.sleep(1)
        try:
            c.noop()
        except (imaplib.IMAP4.abort, OSError) as e:
            print(f"CERRADA {int(time.time() - inicio)}s {e}", flush=True)
            return
    print("ABIERTA", flush=True)
    c.logout()


class HTTPSNombre(http.client.HTTPSConnection):
    """HTTPS que verifica el nombre del certificado y no la direccion conectada."""

    def __init__(self, host, port, contexto, nombre):
        super().__init__(host, port, context=contexto, timeout=30)
        self._nombre = nombre

    def connect(self):
        sock = socket.create_connection((self.host, self.port), self.timeout)
        self.sock = self._context.wrap_socket(sock, server_hostname=self._nombre)


def orden_doveadm(args):
    """Llama al API HTTP de doveadm con la clave que llega por la entrada estandar (con
    --sin-clave, sin cabecera Authorization). Escribe HTTP <codigo> <cuerpo>."""
    cabeceras = {"Content-Type": "application/json"}
    if not args.sin_clave:
        clave = sys.stdin.readline().strip()
        cabeceras["Authorization"] = "X-Dovecot-API " + base64.b64encode(clave.encode()).decode()
    conn = HTTPSNombre(args.doveadm_host, args.doveadm_puerto, contexto(args), args.nombre)
    conn.request("POST", "/doveadm/v1", body=args.ordenes.encode(), headers=cabeceras)
    r = conn.getresponse()
    print(f"HTTP {r.status} {r.read().decode('utf-8', 'replace')}")
    conn.close()


def orden_enviar(args):
    msg = EmailMessage()
    msg["From"] = args.de
    msg["To"] = args.para
    msg["Subject"] = args.asunto
    msg["Date"] = email.utils.formatdate(localtime=False)
    msg["Message-ID"] = email.utils.make_msgid(domain="e2e.test")
    msg.set_content("Mensaje de prueba de los motores de Core Force Mail.\n")
    if args.eicar:
        msg.add_attachment(EICAR.encode(), maintype="application", subtype="octet-stream", filename="eicar.com")
    try:
        s = SMTPTLS(args.smtp_host, args.smtp_puerto, args.nombre)
        s.ehlo()
        s.starttls(context=contexto(args))
        s.ehlo()
        s.login(args.login, args.clave)
    except smtplib.SMTPAuthenticationError as e:
        print(f"RECHAZO AUTH {e.smtp_code} {e.smtp_error.decode('utf-8', 'replace')}")
        return
    codigo, texto = s.mail(args.de)
    if codigo != 250:
        print(f"RECHAZO MAIL {codigo} {texto.decode('utf-8', 'replace')}")
        s.quit()
        return
    codigo, texto = s.rcpt(args.para)
    if codigo not in (250, 251):
        print(f"RECHAZO RCPT {codigo} {texto.decode('utf-8', 'replace')}")
        s.quit()
        return
    # data() solo lanza si el servidor no acepta DATA; la respuesta al final del mensaje
    # (aqui decide el milter) la devuelve.
    try:
        codigo, texto = s.data(msg.as_bytes())
    except smtplib.SMTPDataError as e:
        codigo, texto = e.smtp_code, e.smtp_error
    s.quit()
    if codigo != 250:
        print(f"RECHAZO DATA {codigo} {texto.decode('utf-8', 'replace')}")
        return
    print(f"OK {codigo} {texto.decode('utf-8', 'replace')}")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--ca", default="/ca.pem")
    p.add_argument("--nombre", default="")
    p.add_argument("--imap-host", default="dovecot")
    p.add_argument("--imap-puerto", type=int, default=993)
    p.add_argument("--smtp-host", default="postfix")
    p.add_argument("--smtp-puerto", type=int, default=587)
    p.add_argument("--doveadm-host", default="dovecot")
    p.add_argument("--doveadm-puerto", type=int, default=8443)
    sub = p.add_subparsers(dest="orden", required=True)

    o = sub.add_parser("login")
    o.add_argument("usuario")
    o.add_argument("clave")
    o.add_argument("--maestro", default="")
    o.set_defaults(fn=orden_login)

    o = sub.add_parser("buscar")
    o.add_argument("usuario")
    o.add_argument("clave")
    o.add_argument("token")
    o.add_argument("--maestro", default="")
    o.add_argument("--carpeta", default="INBOX")
    o.add_argument("--espera", type=int, default=60)
    o.add_argument("--estable", type=int, default=0)
    o.set_defaults(fn=orden_buscar)

    o = sub.add_parser("sesion")
    o.add_argument("usuario")
    o.add_argument("clave")
    o.add_argument("--maestro", default="")
    o.add_argument("--limite", type=int, default=60)
    o.set_defaults(fn=orden_sesion)

    o = sub.add_parser("doveadm")
    o.add_argument("ordenes")
    o.add_argument("--sin-clave", action="store_true")
    o.set_defaults(fn=orden_doveadm)

    o = sub.add_parser("enviar")
    o.add_argument("login")
    o.add_argument("clave")
    o.add_argument("de")
    o.add_argument("para")
    o.add_argument("asunto")
    o.add_argument("--eicar", action="store_true")
    o.set_defaults(fn=orden_enviar)

    o = sub.add_parser("eicar")
    o.set_defaults(fn=lambda _args: sys.stdout.write(EICAR))

    args = p.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
