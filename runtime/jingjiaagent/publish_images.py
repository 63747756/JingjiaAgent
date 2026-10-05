"""Explicit publication entry point; ordinary source builds never push images."""
import argparse
import json
import subprocess
from build_metadata import image_tag, load_lock, validate_image

parser = argparse.ArgumentParser()
parser.add_argument('--registry', required=True)
args = parser.parse_args()
registry = args.registry.rstrip('/')
if not registry or any(c.isspace() for c in registry) or '://' in registry:
    raise SystemExit('Provide a registry/repository prefix')
lock = load_lock()
for component in ('daemon', 'guest', 'backend', 'frontend'):
    tag = image_tag(component, lock)
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
    validate_image(component, image, lock)
    target = registry + '/' + tag
    subprocess.run(['docker', 'tag', image['Id'], target], check=True)
    subprocess.run(['docker', 'push', target], check=True)
