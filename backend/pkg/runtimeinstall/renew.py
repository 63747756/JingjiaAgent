"""Renew this node's TLS leaf using its persisted private key; no user session."""
import base64
import http.client
import ipaddress
import json
import pathlib
import socket
import subprocess
import time
import urllib.request
import urllib.parse

root = pathlib.Path('/run/jingjiaagent')

class LocalDocker(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(10)
        self.sock.connect('/var/run/docker.sock')

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None

def renew():
    pending = root/'node.next.crt'
    needs = subprocess.run(['openssl','x509','-checkend',str(30*86400),'-noout','-in',str(root/'node.crt')], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode
    if needs and not pending.exists():
        hostname = urllib.parse.urlsplit((root/'node.endpoint').read_text()).hostname
        try:
            ipaddress.ip_address(hostname)
            san = 'IP:'+hostname
        except ValueError:
            san = 'DNS:'+hostname
        subprocess.run(['openssl','req','-new','-x509','-key',str(root/'node.key'),'-out',str(pending),'-days','365','-subj','/CN='+(root/'node.id').read_text(),'-addext','subjectAltName='+san],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        pending.chmod(0o600)
    if pending.exists():
        node = (root/'node.id').read_text()
        certificate = pending.read_text()
        signature = subprocess.check_output(['openssl','dgst','-sha256','-sign',str(root/'node.key')],input=('jingjiaagent-renew:'+node+':'+certificate).encode(),stderr=subprocess.PIPE)
        data = json.dumps({'node_id':node,'certificate':certificate,'signature':base64.b64encode(signature).decode()}).encode()
        request = urllib.request.Request((root/'node.platform').read_text().rstrip('/')+'/api/v1/users/hosts/renew-certificate',data=data,headers={'Content-Type':'application/json','Authorization':'Bearer '+(root/'node.token').read_text().strip()})
        with urllib.request.build_opener(NoRedirect()).open(request,timeout=30) as response:
            result = json.load(response)
        if result.get('code') != 0:
            raise RuntimeError('certificate renewal rejected')
        (root/'reload.pending').touch(mode=0o600)
        pending.replace(root/'node.crt')
        (root/'node.ca').write_text(certificate)
    if (root/'reload.pending').exists():
        connection=LocalDocker('localhost')
        try:
            node=(root/'node.id').read_text().replace('-','')
            connection.request('POST','/containers/jingjiaagent-node-'+node+'-proxy/kill?signal=HUP')
            response=connection.getresponse()
            if response.status != 204:
                raise RuntimeError('TLS proxy reload deferred')
            response.read()
            (root/'reload.pending').unlink()
        finally:
            connection.close()

if __name__ == '__main__':
    while True:
        try:
            renew()
        except Exception:
            # Never log headers, private keys, HTTP bodies, or subprocess args.
            print('Node certificate maintenance deferred; will retry.',flush=True)
        time.sleep(300)
