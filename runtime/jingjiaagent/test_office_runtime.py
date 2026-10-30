"""Offline package policy and generation tests; use converter smoke for rendering."""
import importlib.util
import io
import json
import hashlib
from pathlib import Path
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
import zipfile

ROOT=Path(__file__).resolve().parent


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path); value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value


converter=module('office_converter',ROOT/'office/converter.py')


class OfficePackageTests(unittest.TestCase):
    def test_fixture_protocols_and_unknown_routes(self):
        fixture=module('work_fixture',ROOT/'tests/work_model_fixture.py')
        server=fixture.ThreadingHTTPServer(('127.0.0.1',0),fixture.Handler)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            base='http://127.0.0.1:'+str(server.server_port)
            request=urllib.request.Request(base+'/v1/unsupported',data=json.dumps({'model':'office-fixture','input':[]}).encode(),headers={'Content-Type':'application/json'})
            with self.assertRaises(urllib.error.HTTPError) as failure:client.open(request,timeout=5)
            self.assertEqual(failure.exception.code,400)
            for route,payload,terminal in (
                ('/v1/responses',{'model':'office-fixture','input':[],'stream':True},'response.completed'),
                ('/v1/messages',{'model':'office-fixture','messages':[],'stream':True},'message_stop'),
            ):
                request=urllib.request.Request(base+route,data=json.dumps(payload).encode(),headers={'Content-Type':'application/json'})
                with client.open(request,timeout=5) as response:
                    self.assertEqual(response.headers.get_content_type(),'text/event-stream')
                    events=[json.loads(line[6:]) for line in response.read().decode().splitlines() if line.startswith('data: {')]
                    self.assertEqual(events[-1]['type'],terminal)
            payload={'model':'office-fixture','messages':[{'role':'user','content':'fixture'}],'stream':True,
                'tools':[{'type':'function','function':{'name':'bash','parameters':{'type':'object'}}}]}
            request=urllib.request.Request(base+'/v1/chat/completions',data=json.dumps(payload).encode(),headers={'Content-Type':'application/json'})
            with client.open(request,timeout=5) as response:
                events=[json.loads(line[6:]) for line in response.read().decode().splitlines() if line.startswith('data: {')]
                self.assertEqual(events[0]['choices'][0]['delta']['tool_calls'][0]['function']['name'],'bash')
                self.assertEqual(events[-1]['choices'][0]['finish_reason'],'tool_calls')
        finally:
            server.shutdown();server.server_close();thread.join(timeout=5)

    def test_format_and_archive_boundaries(self):
        with tempfile.TemporaryDirectory() as directory:
            source=Path(directory)/'source.docx'
            for attack in ('../escape','word/link','word/vbaProject.bin','word/activeX/item.bin'):
                with self.subTest(attack=attack):
                    with zipfile.ZipFile(source,'w') as archive:
                        archive.writestr('[Content_Types].xml','<Types/>');archive.writestr('word/document.xml','<document/>')
                        header=zipfile.ZipInfo(attack);header.external_attr=(0o120777 if attack=='word/link' else 0o100600)<<16
                        archive.writestr(header,'attack')
                    with self.assertRaises(converter.ConversionError):converter.validate_input(source,'docx')
            for relation in ('<Relationship TargetMode="External" Target="http://example.com"/>',
                             '<Relationship TargetMode="&#x45;xternal" Target="file:///run/secrets/converter_token"/>',
                             '<!DOCTYPE x [<!ENTITY secret SYSTEM "file:///run/secrets/converter_token">]><Relationships/>'):
                with zipfile.ZipFile(source,'w') as archive:
                    archive.writestr('[Content_Types].xml','<Types/>');archive.writestr('word/document.xml','<document/>')
                    archive.writestr('word/_rels/document.xml.rels',relation)
                with self.assertRaises(converter.ConversionError):converter.validate_input(source,'docx')
            with self.assertRaises(converter.ConversionError):converter.validate_input(source,'xlsx')

    def test_official_skill_archives_are_reproducible(self):
        package=module('skill_package',ROOT/'package_work_skills.py')
        with tempfile.TemporaryDirectory() as directory:
            one=package.package(Path(directory)/'one');two=package.package(Path(directory)/'two')
            self.assertEqual(one,two);self.assertEqual(len(one['skills']),6)
            for record in one['skills']:
                with zipfile.ZipFile(Path(directory)/'one'/record['archive']) as archive:self.assertIn('SKILL.md',archive.namelist())

    def test_preview_skill_matches_embedded_backend_release(self):
        package=module('skill_package',ROOT/'package_work_skills.py')
        backend=ROOT.parents[1]/'backend/biz/work/release'
        with tempfile.TemporaryDirectory() as directory:
            destination=Path(directory)
            (destination/'manifest.json').write_bytes((backend/'manifest.json').read_bytes())
            package.package_backend_preview(destination)
            self.assertEqual((destination/'deploy-website.zip').read_bytes(),(backend/'deploy-website.zip').read_bytes())
            actual=json.loads((destination/'manifest.json').read_text(encoding='utf-8'))
            expected=json.loads((backend/'manifest.json').read_text(encoding='utf-8'))
            self.assertEqual({p['id']:p for p in actual['packages']},{p['id']:p for p in expected['packages']})

    def test_embedded_office_instructions_match_guest_sources_and_rebuild(self):
        package=module('skill_package',ROOT/'package_work_skills.py')
        backend=ROOT.parents[1]/'backend/biz/work/release'
        with tempfile.TemporaryDirectory() as directory:
            destination=Path(directory)
            (destination/'manifest.json').write_bytes((backend/'manifest.json').read_bytes())
            package.package_backend_office(destination)
            once=(destination/'manifest.json').read_bytes()
            package.package_backend_office(destination)
            self.assertEqual(once,(destination/'manifest.json').read_bytes())
            records={p['id']:p for p in json.loads(once)['packages']}
            for name,source in package.BACKEND_OFFICE.items():
                archive=destination/(name+'.zip')
                self.assertEqual(archive.read_bytes(),(backend/archive.name).read_bytes())
                self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(),records[name]['sha256'])
                with zipfile.ZipFile(archive) as skill:
                    text=skill.read('SKILL.md').decode('utf-8')
                expected=(ROOT/'skills'/source/'SKILL.md').read_text(encoding='utf-8').replace('\nname: '+source+'\n','\nname: '+name+'\n',1)
                self.assertEqual(text,expected)
                self.assertIn('项目包源配置优先',text)
                self.assertIn('默认 Nexus',text)
                self.assertIn('后续修改',text)
                self.assertIn('已完成预览校验',text)

    def test_browser_skill_is_identical_in_backend_and_offline_bundle(self):
        package=module('skill_package',ROOT/'package_work_skills.py')
        backend=ROOT.parents[1]/'backend/biz/work/release'
        with tempfile.TemporaryDirectory() as directory:
            destination=Path(directory)
            original=json.loads((backend/'manifest.json').read_text(encoding='utf-8'))
            original['packages']=[p for p in original['packages'] if p['id']!='browser-automation']
            (destination/'manifest.json').write_text(json.dumps(original),encoding='utf-8')
            # Repeated generation must not duplicate registrations or alter
            # the established office / preview catalog.
            package.package_backend_browser(destination)
            once=(destination/'manifest.json').read_bytes()
            package.package_backend_browser(destination)
            self.assertEqual(once,(destination/'manifest.json').read_bytes())
            manifest=json.loads(once)
            self.assertEqual([p for p in manifest['packages'] if p['id']!='browser-automation'],original['packages'])
            browser=next(p for p in manifest['packages'] if p['id']=='browser-automation')
            self.assertEqual(browser['version'],'1.0.2')
            embedded=(backend/'browser-automation.zip').read_bytes()
            self.assertEqual((destination/'browser-automation.zip').read_bytes(),embedded)
            expected=json.loads((backend/'manifest.json').read_text(encoding='utf-8'))
            # A first registration has no previous release history to preserve.
            # Package identity and bytes must still match the embedded release.
            for catalog in (manifest,expected):
                next(p for p in catalog['packages'] if p['id']=='browser-automation').pop('previous_sha256',None)
            self.assertEqual(manifest,expected)
            bundled=package.package(destination/'offline')
            record=next(p for p in bundled['skills'] if p['name']=='browser-automation')
            self.assertEqual(record['sha256'],browser['sha256'])
            self.assertEqual((destination/'offline'/record['archive']).read_bytes(),embedded)

    def test_missing_skill_source_cannot_produce_empty_offline_package(self):
        package=module('skill_package',ROOT/'package_work_skills.py')
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            with self.assertRaisesRegex(ValueError,'missing SKILL.md'):
                package.write_archive(root/'absent',root/'skill.zip')
            self.assertFalse((root/'skill.zip').exists())


if __name__=='__main__':unittest.main()
