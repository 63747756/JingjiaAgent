"""Exercise the installed CLI and Chromium with Docker --network none.

The HTTP fixture and browser are real; this test does not call an external LLM.
"""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import threading
import time
import unittest


WORK = Path('/workspace/.jingjiaagent/work/browser')
OUTPUT = Path('/workspace/.jingjiaagent/output/browser')
CSV = '项目,金额\n采购,12\n维护,8\n'.encode()
HTML = '''<!doctype html><meta charset="utf-8"><title>Browser offline acceptance</title>
<style>body {font:18px sans-serif; margin:32px; background:#eef4ff} button {padding:12px}</style>
<h1>离线浏览器验收</h1>
<form onsubmit="event.preventDefault(); document.querySelector('output').textContent='已提交：'+this.name.value">
<label>姓名<input name="name" required></label><button>提交</button></form><output></output>
<label>上传 CSV<input type="file" onchange="this.files[0].text().then(t=>document.querySelector('#uploaded').textContent=t)"></label>
<pre id="uploaded"></pre><a href="/download" download="result.csv">下载结果</a>
<script>console.error('browser-fixture-console');</script>'''.encode()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass

    def do_GET(self):
        content = CSV if self.path == '/download' else HTML
        self.send_response(200)
        self.send_header('Content-Type', 'text/csv; charset=utf-8' if self.path == '/download' else 'text/html; charset=utf-8')
        self.send_header('Content-Length', str(len(content)))
        self.end_headers()
        self.wfile.write(content)


class BrowserSmoke(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Match the production workspace's private directories, not Docker's
        # initial empty /workspace with permissive default modes.
        for relative in ('.jingjiaagent', '.jingjiaagent/work', '.jingjiaagent/output'):
            path = Path('/workspace') / relative
            path.mkdir(parents=True, exist_ok=True)
            path.chmod(0o700)
        cls.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.url = f'http://127.0.0.1:{cls.server.server_port}'
        cls.env = dict(os.environ, PATH='/usr/local/bin:/usr/bin:/bin',
                       OPENAI_API_KEY='browser-must-not-inherit-this-test-value')

    @classmethod
    def tearDownClass(cls):
        for name in ('work', 'other'):
            subprocess.run(['playwright-cli', f'-s={name}', 'close'], capture_output=True, timeout=20)
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join(timeout=5)

    def cli(self, *arguments, session='work', success=True):
        options = [f'-s={session}'] if session else []
        result = subprocess.run(['playwright-cli', *options, *arguments, '--json'],
                                env=self.env, text=True, capture_output=True, timeout=45)
        try: payload = json.loads(result.stdout)
        except json.JSONDecodeError: self.fail(result.stdout + result.stderr)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertFalse(payload.get('isError'), result.stdout)
        else:
            self.assertTrue(result.returncode or payload.get('isError'), result.stdout)
        return payload

    def evaluate(self, code, session='work'):
        return json.loads(self.cli('eval', code, session=session)['result'])

    def snapshot(self):
        payload = self.cli('snapshot', '--filename=' + str(WORK / 'acceptance.yml'))
        path = Path(payload['snapshot']['file'])
        if not path.is_absolute(): path = WORK / path
        self.assertTrue(path.resolve().is_relative_to(WORK))
        return path.read_text()

    def ref(self, snapshot, role, name):
        match = re.search(role + ' "' + re.escape(name) + r'"[^\n]*\[ref=(e\d+)\]', snapshot)
        self.assertIsNotNone(match, snapshot)
        return match.group(1)

    def check_private_script_inputs(self):
        # Files written after open do not inherit the already-prepared directory
        # permissions. Grant only the explicitly requested script, not its peers.
        self.assertEqual(WORK.stat().st_uid, 1001)
        absolute = WORK / 'agent-absolute.js'
        relative = WORK / 'agent-relative.js'
        failing = WORK / 'agent-failing.js'
        neighbor = WORK / 'agent-private.txt'
        scripts = {
            absolute: 'async page => { await page.locator("output").evaluate(el => el.textContent = "absolute-script"); }',
            relative: 'async page => { await page.locator("output").evaluate(el => el.textContent = "relative-script"); }',
            failing: 'async page => { throw new Error("browser-private-script-error"); }',
            neighbor: 'unselected private contents',
        }
        previous_umask = os.umask(0o077)
        try:
            for path, content in scripts.items():
                path.write_text(content)
        finally:
            os.umask(previous_umask)
        for path in scripts:
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.stat().st_uid, 0)
        original = {path: (path.stat().st_uid, path.stat().st_gid,
                           path.stat().st_mode, path.read_bytes()) for path in scripts}
        staged_before = set(Path('/tmp').glob('jingjiaagent-browser-script-*'))

        self.cli('run-code', '--filename=' + str(absolute))
        self.assertEqual(self.evaluate('() => document.querySelector("output").textContent'), 'absolute-script')
        self.assertEqual(set(Path('/tmp').glob('jingjiaagent-browser-script-*')), staged_before)
        # CLI relative filenames are relative to its fixed browser work cwd,
        # even though the Agent calls the wrapper from another directory.
        self.cli('run-code', '--filename', relative.name)
        self.assertEqual(self.evaluate('() => document.querySelector("output").textContent'), 'relative-script')
        self.assertEqual(set(Path('/tmp').glob('jingjiaagent-browser-script-*')), staged_before)
        failure = self.cli('run-code', '--filename=' + str(failing), success=False)
        self.assertIn('browser-private-script-error', json.dumps(failure))
        self.assertEqual(set(Path('/tmp').glob('jingjiaagent-browser-script-*')), staged_before)

        for path, properties in original.items():
            self.assertEqual((path.stat().st_uid, path.stat().st_gid,
                              path.stat().st_mode, path.read_bytes()), properties)
        hidden = subprocess.run(['python3', '-c', 'from pathlib import Path; import sys; Path(sys.argv[1]).read_bytes()',
                                 str(neighbor)], user=1001, group=1001, extra_groups=[], capture_output=True)
        self.assertNotEqual(hidden.returncode, 0, 'selecting a script must not expose neighboring files')

        outside = Path('/tmp/browser-outside-workspace.js')
        outside.write_text('async page => { await page.locator("output").evaluate(el => el.textContent = "escaped"); }')
        outside.chmod(0o644)
        try:
            # Wrapper validation errors are plain stderr, not CLI JSON replies.
            rejected = subprocess.run(['playwright-cli', 'run-code', '--filename=' + str(outside), '--json'],
                                      env=self.env, text=True, capture_output=True, timeout=45)
            self.assertNotEqual(rejected.returncode, 0, rejected.stdout + rejected.stderr)
            self.assertIn('Browser setup failed:', rejected.stderr)
            self.assertEqual(self.evaluate('() => document.querySelector("output").textContent'), 'relative-script')
            self.assertEqual(set(Path('/tmp').glob('jingjiaagent-browser-script-*')), staged_before)
        finally:
            outside.unlink(missing_ok=True)

    def test_browser_workflow_without_network_or_preview(self):
        version = subprocess.check_output(['playwright-cli', '--version'], text=True).strip()
        self.assertIn('0.1.22', version)
        self.cli('open', self.url)
        self.check_private_script_inputs()
        snapshot = self.snapshot()
        self.assertIn('离线浏览器验收', snapshot)
        submit = self.ref(snapshot, 'button', '提交')
        self.cli('click', submit)
        self.assertFalse(self.evaluate('() => document.querySelector("input").checkValidity()'))
        self.cli('fill', self.ref(self.snapshot(), 'textbox', '姓名'), 'open upload')
        self.cli('click', self.ref(self.snapshot(), 'button', '提交'))
        self.assertEqual(self.evaluate('() => document.querySelector("output").textContent'), '已提交：open upload')
        self.cli('resize', '1024', '768')
        self.cli('screenshot', '--filename=' + str(OUTPUT / 'desktop.png'))
        self.cli('resize', '390', '844')
        self.cli('screenshot', '--filename=' + str(OUTPUT / 'mobile.png'))
        for name, size in [('desktop.png', (1024, 768)), ('mobile.png', (390, 844))]:
            data = (OUTPUT / name).read_bytes()
            self.assertEqual(data[:8], b'\x89PNG\r\n\x1a\n')
            self.assertEqual(struct.unpack('>II', data[16:24]), size)
        console = self.cli('console', 'error')
        self.assertIn('browser-fixture-console', json.dumps(console) + '\n'.join(
            p.read_text(errors='replace') for p in WORK.glob('console-*.log')))

        # Attachments are root-owned 0600 inside a 0700 tree, just like production.
        inputs = Path('/workspace/.jingjiaagent/attachments/browser-smoke')
        inputs.mkdir(parents=True, exist_ok=True)
        inputs.chmod(0o700)
        source = inputs / 'input.csv'
        source.write_bytes(CSV)
        source.chmod(0o600)
        self.cli('click', self.ref(self.snapshot(), 'button', '上传 CSV'))
        self.cli('upload', str(source), '-s', 'work', session=None)
        self.assertEqual(self.evaluate('() => document.querySelector("#uploaded").textContent'), CSV.decode())
        self.assertEqual(source.stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(WORK.glob('upload-*')), [])
        target = OUTPUT / 'export.csv'
        self.cli('run-code', 'async page => { const waiting = page.waitForEvent("download"); '
                 'await page.getByRole("link", {name:"下载结果"}).click(); '
                 f'await (await waiting).saveAs({json.dumps(str(target))}); }}')
        self.assertEqual(hashlib.sha256(target.read_bytes()).digest(), hashlib.sha256(CSV).digest())

        self.cli('eval', '() => { localStorage.setItem("session-check", "original"); return true; }')
        self.cli('open', self.url, session='other')
        self.assertIsNone(self.evaluate('() => localStorage.getItem("session-check")', session='other'))
        self.cli('close', session='other')
        self.assertEqual(self.evaluate('() => localStorage.getItem("session-check")'), 'original')
        self.cli('click', 'e999999', success=False)
        self.assertIn('离线浏览器验收', self.snapshot())

        browser_pids = []
        daemon_checked = False
        for process in Path('/proc').glob('[0-9]*'):
            try:
                command = (process / 'cmdline').read_bytes()
                if b'playwright-core/lib/entry/cliDaemon.js' in command:
                    # /proc environ is readable by the same uid, not Docker root
                    # without CAP_SYS_PTRACE. Do not grant extra container caps.
                    environment = subprocess.check_output(['python3', '-c',
                        'from pathlib import Path; import sys; sys.stdout.buffer.write(Path(sys.argv[1]).read_bytes())',
                        str(process / 'environ')], user=1001, group=1001, extra_groups=[])
                    self.assertNotIn(b'browser-must-not-inherit-this-test-value', environment)
                    self.assertIn(b'NO_UPDATE_NOTIFIER=1', environment)
                    daemon_checked = True
                if b'/opt/jingjiaagent/browsers/' not in command: continue
                browser_pids.append(process)
                self.assertIn('Uid:\t1001\t1001\t1001\t1001', (process / 'status').read_text())
                self.assertNotIn(b'--remote-debugging-port', command)
            except FileNotFoundError: pass
        self.assertTrue(browser_pids, 'Chromium must actually be running')
        self.assertTrue(daemon_checked, 'CLI daemon must use the filtered environment')
        # Only explicitly delivered files belong to the artifact directory.
        self.assertEqual({p.name for p in OUTPUT.iterdir()}, {'desktop.png', 'mobile.png', 'export.csv'})
        self.cli('close')
        for _ in range(30):
            if not any(p.exists() and (p / 'cmdline').read_bytes() for p in browser_pids): break
            time.sleep(.1)
        self.assertFalse(any(p.exists() and (p / 'cmdline').read_bytes() for p in browser_pids))
        # Checkpoint ZIP extraction restores managed files under root/0600.
        # The next browser must be able to read/replace its own former outputs.
        for folder in (WORK, OUTPUT):
            for path in folder.rglob('*'):
                if not path.is_symlink():
                    os.chown(path, 0, 0)
                    path.chmod(0o700 if path.is_dir() else 0o600)
            os.chown(folder, 0, 0)
            folder.chmod(0o700)
        (WORK / 'private-link').symlink_to(source)
        self.cli('open', self.url)
        self.assertIsNone(self.evaluate('() => localStorage.getItem("session-check")'))
        self.cli('screenshot', '--filename=' + str(OUTPUT / 'desktop.png'))
        self.assertTrue((OUTPUT / 'desktop.png').read_bytes().startswith(b'\x89PNG'))
        hidden = subprocess.run(['python3', '-c', 'from pathlib import Path; import sys; Path(sys.argv[1]).read_bytes()',
                                 str(source)], user=1001, group=1001, extra_groups=[], capture_output=True)
        self.assertNotEqual(hidden.returncode, 0, 'restored browser links must not expose private attachments')
        self.cli('close')
        print('PASS: real Chromium, private attachments, desktop/mobile, download hash, '
              'session isolation, cleanup, restored files, non-root and credential isolation; no preview/Internet')


if __name__ == '__main__': unittest.main(verbosity=2)
