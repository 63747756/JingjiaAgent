"""Resolve the locked registry identity on the current builder's image store."""
import json
import re
import subprocess


def prepare_proxy(lock):
    refs = lock.get('repo_digests', [])
    if len(refs) != 1 or not re.fullmatch(r'[^\s@]+@sha256:[a-f0-9]{64}', refs[0]):
        raise ValueError('Proxy lock requires one immutable registry digest')
    ref = refs[0]
    try:
        raw = subprocess.check_output(['docker', 'image', 'inspect', ref], stderr=subprocess.DEVNULL)
    except subprocess.CalledProcessError:
        subprocess.run(['docker', 'pull', '--platform', lock['platform'], ref], check=True)
        raw = subprocess.check_output(['docker', 'image', 'inspect', ref])
    image = json.loads(raw)[0]
    if (image['Os'] + '/' + image['Architecture'] != lock['platform']
            or ref not in image.get('RepoDigests', [])
            or not re.fullmatch(r'sha256:[a-f0-9]{64}', image['Id'])):
        raise ValueError('Proxy image does not match its locked registry identity')
    return image['Id']
