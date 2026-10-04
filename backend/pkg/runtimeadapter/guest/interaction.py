"""Answer only a correlated native request through its private Guest server."""
import base64
import fcntl
import hashlib
import json
import os
import pathlib
import re
import sys
import time
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = pathlib.Path('/data/state/monkeycode-interactions')
LOCAL_HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def native_receipt(target, req):
    proof = target.with_name(target.stem + '.native.json')
    if not proof.exists():
        return False
    event = json.loads(proof.read_text())
    data, kind = event['data'], event['kind']
    if data.get('sessionID') != req['session_id'] or data.get('requestID') != req['request_id']:
        return False
    if req['request_kind'] == 'question':
        return (kind in ('question.rejected', 'question.v2.rejected') if req.get('cancelled') else
                kind in ('question.replied', 'question.v2.replied') and data.get('answers') == req['answers'])
    reply = 'reject' if req.get('cancelled') or req.get('answers') == [['拒绝']] else 'once'
    return kind in ('permission.replied', 'permission.v2.replied') and data.get('reply') == reply


def save(stream, record):
    stream.seek(0)
    json.dump(record, stream)
    stream.truncate()
    stream.flush()
    os.fsync(stream.fileno())


def sdk_receipt(target, fingerprint):
    proof = target.with_name(target.stem + '.consumed.json')
    return proof.exists() and json.loads(proof.read_text()).get('receipt') == fingerprint


def sdk_answer(target, req, fingerprint):
    response = target.with_name(target.stem + '.answer.json')
    if response.exists():
        if json.loads(response.read_text()).get('receipt') != fingerprint:
            raise ValueError('SDK response replay conflict')
        return
    fd, temporary = tempfile.mkstemp(dir=target.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as output:
            json.dump(dict(receipt=fingerprint, answer=req), output)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, response)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def control(req):
    run, identifier = req.get('run_id', ''), req.get('request_id', '')
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', run) or not re.fullmatch(r'(que|per)[A-Za-z0-9_-]+', identifier):
        raise ValueError('invalid native request')
    target = ROOT / run / (identifier + '.json')
    with open(target, 'r+', encoding='utf-8') as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        record = json.load(stream)
        expected = record['request']
        if any(req.get(key) != expected.get(key) for key in ('run_id', 'request_id', 'session_id', 'request_kind')):
            raise ValueError('native request ownership mismatch')
        fingerprint = hashlib.sha256(json.dumps(req, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        if record.get('receipt'):
            if record['receipt'] != fingerprint:
                raise ValueError('native response replay conflict')
            return dict(success=True)
        # Native APIs do not support idempotency keys. A lost success response
        # must stay uncertain; absence of the request cannot prove acceptance.
        uncertain = record.get('uncertain')
        if uncertain and uncertain != fingerprint:
            raise ValueError('native response replay conflict')
        if uncertain and record.get('backend') == 'sdk' and sdk_receipt(target, fingerprint):
            record.pop('uncertain', None)
            record['receipt'] = fingerprint
            save(stream, record)
            return dict(success=True)
        if uncertain and native_receipt(target, req):
            record.pop('uncertain', None)
            record['receipt'] = fingerprint
            save(stream, record)
            return dict(success=True)
        try:
            active = json.loads((ROOT / (run + '.json')).read_text())
        except FileNotFoundError:
            if uncertain:
                raise RuntimeError('native response requires reconciliation') from None
            raise ValueError('native request expired') from None
        if active.get('session_id') != req['session_id'] or active.get('run_id') != run:
            raise ValueError('native request is no longer active')
        kind = req['request_kind']
        if kind not in ('question', 'permission'):
            raise ValueError('invalid request kind')
        if record.get('backend') == 'sdk':
            if active.get('backend') != 'sdk' or active.get('provider') != record.get('provider'):
                raise ValueError('SDK request owner mismatch')
            os.kill(active['pid'], 0)
            if kind == 'permission' and not req.get('cancelled') and req.get('answers') not in ([['允许一次']], [['拒绝']]):
                raise ValueError('invalid SDK permission decision')
            record['uncertain'] = fingerprint
            save(stream, record)
            sdk_answer(target, req, fingerprint)
            deadline = time.monotonic() + 4
            while time.monotonic() < deadline:
                if sdk_receipt(target, fingerprint):
                    record.pop('uncertain', None)
                    record['receipt'] = fingerprint
                    save(stream, record)
                    return dict(success=True)
                time.sleep(0.05)
            raise RuntimeError('SDK response requires reconciliation')
        auth = base64.b64encode(('opencode:' + active['password']).encode()).decode()

        def request(route, body=None):
            url = 'http://127.0.0.1:' + str(active['port']) + route + '?' + urllib.parse.urlencode({'directory': active['directory']})
            http = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(),
                                          headers={'Authorization': 'Basic ' + auth, 'Content-Type': 'application/json'})
            with LOCAL_HTTP.open(http, timeout=5) as response:
                data = response.read(1024 * 1024 + 1)
                if len(data) > 1024 * 1024:
                    raise ValueError('native response exceeds limit')
                return json.loads(data) if data else None

        pending = next((x for x in request('/' + kind) if x.get('id') == identifier and x.get('sessionID') == req['session_id']), None)
        if pending is None:
            if uncertain:
                raise RuntimeError('native response requires reconciliation')
            raise ValueError('native request expired')
        cancelled = req.get('cancelled', False)
        if kind == 'question':
            body = {} if cancelled else dict(answers=req['answers'])
            suffix = 'reject' if cancelled else 'reply'
        else:
            answers = req.get('answers', [])
            if not cancelled and (len(answers) != 1 or answers[0] not in (['允许一次'], ['拒绝'])):
                raise ValueError('invalid permission decision')
            body = dict(reply='reject' if cancelled or answers == [['拒绝']] else 'once')
            suffix = 'reply'
        record['uncertain'] = fingerprint
        save(stream, record)
        try:
            accepted = request('/' + kind + '/' + identifier + '/' + suffix, body)
        except urllib.error.HTTPError as error:
            if error.code in (400, 404) and not uncertain:
                record.pop('uncertain', None)
                save(stream, record)
                raise ValueError('native response rejected') from None
            raise
        if accepted is not True:
            raise RuntimeError('native response requires reconciliation')
        record.pop('uncertain', None)
        record['receipt'] = fingerprint
        save(stream, record)
        return dict(success=True)


if __name__ == '__main__':
    try:
        req = json.loads(base64.b64decode(sys.argv[1], validate=True))
        print(json.dumps(dict(data=control(req))))
    except (ValueError, FileNotFoundError):
        print(json.dumps(dict(error='Guest interaction rejected', invalid=True)))
    except Exception:
        print(json.dumps(dict(error='Guest interaction requires reconciliation')))
