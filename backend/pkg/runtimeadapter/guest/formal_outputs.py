"""Refresh only verified canonical outputs before a new work turn."""
import base64
import hashlib
import json
import os
import pathlib
import tempfile
import urllib.request
import sys

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args): return None

def apply(req):
    prepared=[]
    try:
        client=urllib.request.build_opener(NoRedirect())
        for item in req['files']:
            relative=pathlib.PurePosixPath(item['path'])
            if relative.is_absolute() or str(relative)!=item['path'] or relative.parts[0]!='output' or '..' in relative.parts or '\\' in item['path']:
                raise ValueError('invalid output path')
            path=pathlib.Path('/workspace/.jingjiaagent').joinpath(*relative.parts)
            for parent in (path,*path.parents):
                if parent.is_symlink(): raise ValueError('output path has a symlink')
            if not path.is_file(): continue
            digest=hashlib.sha256()
            with path.open('rb') as stream:
                for chunk in iter(lambda:stream.read(65536),b''): digest.update(chunk)
            if digest.hexdigest()==item['sha256']: continue
            if digest.hexdigest()!=item['source_sha256']: continue
            fd,name=tempfile.mkstemp(prefix='.canonical-',dir=path.parent)
            temporary=pathlib.Path(name); prepared.append((temporary,path))
            digest=hashlib.sha256();size=0
            with os.fdopen(fd,'wb') as stream,client.open(item['url'],timeout=20) as source:
                for chunk in iter(lambda:source.read(65536),b''):
                    size+=len(chunk)
                    if size>100<<20:raise ValueError('canonical output exceeds limit')
                    digest.update(chunk);stream.write(chunk)
                stream.flush();os.fsync(stream.fileno())
            if size!=item['size'] or digest.hexdigest()!=item['sha256']:raise ValueError('canonical output hash mismatch')
        for temporary,path in prepared:os.replace(temporary,path)
        return {'installed':True}
    finally:
        for temporary,_ in prepared:temporary.unlink(missing_ok=True)

if __name__=='__main__':
    try:print(json.dumps({'data':apply(json.loads(base64.b64decode(sys.argv[1],validate=True)))}))
    except Exception:print(json.dumps({'error':'Formal output refresh rejected'}))
