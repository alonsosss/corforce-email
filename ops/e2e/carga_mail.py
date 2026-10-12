#!/usr/bin/env python3
"""Carga de envio autenticado contra los motores de la prueba (etapa opcional E2E_CARGA=1 de ops/e2e/mail.sh).

Corre dentro de la red de los motores. Abre --sesiones conexiones SMTP en paralelo contra la
submission, envia --mensajes por sesion al buzon destino y mide la latencia de aceptacion de cada
uno. Despues cuenta por IMAP cuantos llegaron al INBOX del destino y cuanto tardo el ultimo.
Imprime un resumen en JSON: nunca escribe credenciales.
"""
import argparse
import email.utils
import imaplib
import json
import smtplib
import ssl
import statistics
import sys
import threading
import time
import uuid
from collections import Counter
from email.message import EmailMessage


class SMTPTLS(smtplib.SMTP):
    def __init__(self, host, port, nombre):
        super().__init__(host, port, local_hostname="carga.e2e.test", timeout=120)
        self._host = nombre


class IMAPTLS(imaplib.IMAP4_SSL):
    def __init__(self, host, port, contexto, nombre):
        self._nombre = nombre
        super().__init__(host, port, ssl_context=contexto, timeout=60)

    def _create_socket(self, timeout):
        sock = imaplib.IMAP4._create_socket(self, timeout)
        return self.ssl_context.wrap_socket(sock, server_hostname=self._nombre)


def mensaje(de, para, asunto, bytes_cuerpo):
    m = EmailMessage()
    m["From"] = de
    m["To"] = para
    m["Subject"] = asunto
    m["Date"] = email.utils.formatdate(localtime=False)
    m["Message-ID"] = email.utils.make_msgid(domain="carga.e2e.test")
    m.set_content(("Mensaje de carga de los motores de Core Force Mail.\n" * 64)[:bytes_cuerpo])
    return m.as_bytes()


def sesion(args, ctx, numero, token, tiempos, errores, bloqueo):
    try:
        s = SMTPTLS(args.smtp_host, args.smtp_puerto, args.nombre)
        s.ehlo()
        s.starttls(context=ctx)
        s.ehlo()
        s.login(args.login, args.clave)
    except Exception as e:
        with bloqueo:
            errores[f"conexion: {type(e).__name__} {str(e)[:80]}"] += args.mensajes
        return
    for i in range(args.mensajes):
        cuerpo = mensaje(args.de, args.para, f"{token}-{numero}-{i}", args.bytes)
        inicio = time.monotonic()
        try:
            s.sendmail(args.de, [args.para], cuerpo)
            dt = time.monotonic() - inicio
            with bloqueo:
                tiempos.append(dt)
        except smtplib.SMTPResponseException as e:
            with bloqueo:
                errores[f"{e.smtp_code} {e.smtp_error.decode('utf-8', 'replace')[:80]}"] += 1
            if e.smtp_code >= 500 and e.smtp_code != 554:
                break
        except Exception as e:
            with bloqueo:
                errores[f"{type(e).__name__} {str(e)[:80]}"] += 1
            break
    try:
        s.quit()
    except Exception:
        pass


def contar_llegados(args, ctx, token):
    c = IMAPTLS(args.imap_host, args.imap_puerto, ctx, args.nombre)
    c.login(args.destino_login, args.destino_clave)
    c.select("INBOX", readonly=True)
    estado, datos = c.search(None, "SUBJECT", token)
    c.logout()
    return len(datos[0].split()) if estado == "OK" and datos and datos[0] else 0


def percentil(valores, p):
    if not valores:
        return None
    o = sorted(valores)
    return round(o[min(len(o) - 1, int(len(o) * p))], 3)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--ca", default="/ca.pem")
    p.add_argument("--nombre", required=True)
    p.add_argument("--smtp-host", default="postfix")
    p.add_argument("--smtp-puerto", type=int, default=587)
    p.add_argument("--imap-host", default="dovecot")
    p.add_argument("--imap-puerto", type=int, default=993)
    p.add_argument("--login", required=True)
    p.add_argument("--clave", required=True)
    p.add_argument("--de", required=True)
    p.add_argument("--para", required=True)
    p.add_argument("--destino-login", required=True)
    p.add_argument("--destino-clave", required=True)
    p.add_argument("--sesiones", type=int, default=5)
    p.add_argument("--mensajes", type=int, default=20)
    p.add_argument("--bytes", type=int, default=2048)
    p.add_argument("--espera-entrega", type=int, default=120)
    args = p.parse_args()

    ctx = ssl.create_default_context(cafile=args.ca)
    token = "carga" + uuid.uuid4().hex[:8]
    tiempos, errores, bloqueo = [], Counter(), threading.Lock()
    previos = contar_llegados(args, ctx, "carga")

    inicio = time.monotonic()
    hilos = [threading.Thread(target=sesion, args=(args, ctx, n, token, tiempos, errores, bloqueo))
             for n in range(args.sesiones)]
    for h in hilos:
        h.start()
    for h in hilos:
        h.join()
    envio = time.monotonic() - inicio

    aceptados = len(tiempos)
    limite = time.monotonic() + args.espera_entrega
    llegados = 0
    while time.monotonic() < limite:
        llegados = contar_llegados(args, ctx, token)
        if llegados >= aceptados:
            break
        time.sleep(1)
    entrega = time.monotonic() - inicio

    resumen = {
        "token": token,
        "sesiones": args.sesiones,
        "pedidos": args.sesiones * args.mensajes,
        "aceptados": aceptados,
        "rechazados": dict(errores),
        "llegados_al_inbox": llegados,
        "segundos_envio": round(envio, 2),
        "segundos_hasta_ultima_entrega": round(entrega, 2),
        "mensajes_por_segundo_aceptados": round(aceptados / envio, 2) if envio else None,
        "latencia_aceptacion_s": {
            "media": round(statistics.fmean(tiempos), 3) if tiempos else None,
            "p50": percentil(tiempos, 0.5),
            "p95": percentil(tiempos, 0.95),
            "max": round(max(tiempos), 3) if tiempos else None,
        },
        "inbox_previo_con_prefijo_carga": previos,
    }
    print(json.dumps(resumen, indent=2))
    sys.exit(0 if llegados >= aceptados and aceptados > 0 else 1)


if __name__ == "__main__":
    main()
