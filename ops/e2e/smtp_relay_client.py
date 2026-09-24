#!/usr/bin/env python3
"""Cliente SMTP de la seccion del relay de ops/e2e/run.sh.

Habla con smtp-relay como una integracion real: verifica el certificado del relay con el de la
prueba (autofirmado, sirve de ancla) contra su nombre, y escribe una linea por orden: OK <codigo>
<texto>, AUTH-ANUNCIADO si o no, o RECHAZO <fase> <codigo> <texto>.

  smtp_relay_client.py claro <host> <puerto> <nombre> <ca> <usuario> <clave>
  smtp_relay_client.py enviar <host> <puerto> <nombre> <ca> <usuario> <clave> <de> <para,...> [--implicito] [--adjunto-eicar] [--login]
"""
import base64
import smtplib
import socket
import ssl
import sys
from email.message import EmailMessage

# Fichero de prueba estandar de EICAR, partido para que el propio fuente no sea la firma.
EICAR = "X5O!P%@AP[4\\PZX54(P^)7CC)7}$" + "EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"


class SMTPNombre(smtplib.SMTP):
    """SMTP cuyo STARTTLS verifica el nombre del certificado y no la direccion conectada."""

    def __init__(self, host, port, nombre):
        self._nombre = nombre
        super().__init__(host, port, timeout=30)

    def starttls(self, context=None):
        self.ehlo_or_helo_if_needed()
        code, resp = self.docmd("STARTTLS")
        if code != 220:
            raise smtplib.SMTPResponseException(code, resp)
        self.sock = context.wrap_socket(self.sock, server_hostname=self._nombre)
        self.file = None
        self.helo_resp = self.ehlo_resp = None
        self.esmtp_features = {}
        self.does_esmtp = False
        return code, resp


class SMTPImplicito(smtplib.SMTP_SSL):
    def __init__(self, host, port, nombre, contexto):
        self._nombre = nombre
        super().__init__(host, port, context=contexto, timeout=30)

    def _get_socket(self, host, port, timeout):
        sock = socket.create_connection((host, port), timeout)
        return self.context.wrap_socket(sock, server_hostname=self._nombre)


def contexto(ca):
    ctx = ssl.create_default_context(cafile=ca)
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    return ctx


def rechazo(fase, e):
    if isinstance(e, smtplib.SMTPRecipientsRefused):
        code, texto = next(iter(e.recipients.values()))
    elif isinstance(e, smtplib.SMTPResponseException):
        code, texto = e.smtp_code, e.smtp_error
    else:
        print(f"RECHAZO {fase} 0 {e}")
        return
    if isinstance(texto, bytes):
        texto = texto.decode(errors="replace")
    print(f"RECHAZO {fase} {code} {texto}")


def claro(host, port, nombre, ca, usuario, clave):
    s = SMTPNombre(host, int(port), nombre)
    s.ehlo()
    print("AUTH-ANUNCIADO " + ("si" if s.has_extn("auth") else "no"))
    try:
        code, resp = s.docmd("AUTH", "PLAIN " + base64.b64encode(f"\0{usuario}\0{clave}".encode()).decode())
        print(f"RECHAZO auth {code} {resp.decode(errors='replace')}" if code >= 400 else f"OK {code} {resp.decode()}")
        code, resp = s.docmd("MAIL", "FROM:<a@b.test>")
        print(f"RECHAZO mail {code} {resp.decode(errors='replace')}" if code >= 400 else f"OK {code} {resp.decode()}")
    finally:
        s.close()


def enviar(host, port, nombre, ca, usuario, clave, de, para, opciones):
    ctx = contexto(ca)
    if "--implicito" in opciones:
        s = SMTPImplicito(host, int(port), nombre, ctx)
    else:
        s = SMTPNombre(host, int(port), nombre)
        s.ehlo()
        s.starttls(context=ctx)
    try:
        s.ehlo()
        try:
            if "--login" in opciones:
                s.user, s.password = usuario, clave
                s.auth("LOGIN", s.auth_login, initial_response_ok=False)
            else:
                s.login(usuario, clave)
        except Exception as e:
            rechazo("auth", e)
            return
        m = EmailMessage()
        m["From"] = de
        m["To"] = para.split(",")[0]
        m["Subject"] = "Pedido confirmado"
        m["Message-ID"] = f"<e2e-{abs(hash((de, para, tuple(opciones))))}@{de.split('@')[1]}>"
        m.set_content("Gracias por tu compra.")
        if "--adjunto-eicar" in opciones:
            m.add_attachment(EICAR.encode(), maintype="application", subtype="octet-stream", filename="eicar.com")
        try:
            s.send_message(m, from_addr=de, to_addrs=para.split(","))
            print("OK 250 enviado")
        except Exception as e:
            rechazo("data", e)
    finally:
        try:
            s.quit()
        except Exception:
            pass


def main():
    orden, args = sys.argv[1], sys.argv[2:]
    if orden == "claro":
        claro(*args[:6])
    elif orden == "enviar":
        enviar(*args[:8], args[8:])
    else:
        sys.exit(f"orden desconocida: {orden}")


if __name__ == "__main__":
    main()
