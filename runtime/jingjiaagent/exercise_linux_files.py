"""Real original file APIs: 10 MiB boundary, failed transfers and user scope."""
import hashlib
import json
import secrets
import socket
import urllib.error
import urllib.parse
import urllib.request
from linux_web_common import TaskSocket, api, load, login, runtime_rpc, save, sql, wait

owner, actor, jar=login()
member, _, _=login('web-member-account.json')
task=load('capacity-tasks.json')['owner-opencode']
vm=task['vm']
sandbox=sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+vm+"';")
directory='/workspace/phase4-file-boundary'
path=directory+'/边界二进制.bin'
payload=bytes(range(256))*(10*1024*1024//256)
boundary='phase4-'+secrets.token_hex(16)

def query(target):return urllib.parse.urlencode({'id':vm,'path':target})
def multipart(content,name='边界二进制.bin'):
    return (('--'+boundary+'\r\nContent-Disposition: form-data; name="file"; filename="'+name+'"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()
        +content+('\r\n--'+boundary+'--\r\n').encode())
def upload(client,content,target=path):
    request=urllib.request.Request('http://127.0.0.1:47424/api/v1/users/files/upload?'+query(target),
        data=multipart(content),headers={'Content-Type':'multipart/form-data; boundary='+boundary})
    try:
        with client.open(request,timeout=120) as response:return response.status,json.load(response)
    except urllib.error.HTTPError as response:return response.code,json.load(response)
def download(client,target):
    try:
        with client.open('http://127.0.0.1:47424/api/v1/users/files/download?'+query(target),timeout=120) as response:
            return response.read(),response.headers
    except urllib.error.HTTPError as response:return response.read(),response.headers
def exact(target):
    data,headers=download(owner,target)
    if data!=payload or headers.get('Content-Length')!=str(len(payload)):
        raise RuntimeError('Original file content/length mismatch')

with TaskSocket(task['task'],jar,control=True):
    wait(lambda:runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']=='SANDBOX_STATUS_RUNNING',60,label='file fixture original resume')
    folders=api(owner,'/api/v1/users/folders?'+query('/workspace'))
    if not any(item.get('name')=='phase4-file-boundary' for item in (folders or [])):
        # Existing directories are idempotent in the original mkdir API.
        api(owner,'/api/v1/users/folders',{'id':vm,'path':directory})
    status,result=upload(owner,payload)
    if status!=200 or result.get('code')!=0:raise RuntimeError('Exactly 10 MiB upload rejected')
    exact(path)
    status,result=upload(owner,payload+b'x')
    if status!=200 or result.get('code')!=10102:raise RuntimeError('Over-limit original upload accepted')
    exact(path)
    status,result=upload(member,b'foreign-user-data')
    if status==200 and result.get('code')==0:raise RuntimeError('Foreign user overwrote an environment file')
    exact(path)
    data,_=download(member,path)
    try:denied=json.loads(data)
    except ValueError:raise RuntimeError('Foreign user read sandbox file bytes')
    if denied.get('code')==0:raise RuntimeError('Foreign user download accepted')
    # Disconnect halfway through multipart input. The existing complete
    # target must survive; the incomplete transfer is never committed.
    partial=multipart(payload)
    cookie='; '.join(c.name+'='+c.value for c in jar)
    with socket.create_connection(('127.0.0.1',47424),timeout=10) as connection:
        headers='POST /api/v1/users/files/upload?'+query(path)+' HTTP/1.1\r\nHost: 127.0.0.1:47424\r\nCookie: '+cookie+'\r\nContent-Type: multipart/form-data; boundary='+boundary+'\r\nContent-Length: '+str(len(partial))+'\r\nConnection: close\r\n\r\n'
        connection.sendall(headers.encode()+partial[:65536])
        connection.shutdown(socket.SHUT_WR)
    exact(path)
    copied=directory+'/复制中文.bin'
    moved=directory+'/移动中文.bin'
    api(owner,'/api/v1/users/files/copy',{'id':vm,'source':path,'target':copied})
    exact(copied)
    api(owner,'/api/v1/users/files/move',{'id':vm,'source':copied,'target':moved},'PUT')
    exact(moved)
    api(owner,'/api/v1/users/files',{'id':vm,'path':moved},'DELETE')
    data,_=download(owner,moved)
    try:missing=json.loads(data)
    except ValueError:raise RuntimeError('Deleted fixture file still downloadable')
    if missing.get('code')==0:raise RuntimeError('Deleted fixture file was retained')
    exact(path)
save('file-boundary-report.json',{'exact_10_mib':True,'over_limit_denied_without_overwrite':True,
    'interrupted_multipart_preserves_target':True,'cross_user_read_write_denied':True,
    'chinese_copy_move_delete':True,'bytes':len(payload),'sha256':hashlib.sha256(payload).hexdigest()})
print('Actual 10 MiB upload/download, over-limit and interrupted transfer protection, Chinese copy/move/delete and user scope passed.',flush=True)
