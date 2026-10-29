"""Build deterministic, verified offline packages of the official skills."""
import argparse
import hashlib
import json
from pathlib import Path
import zipfile

ROOT = Path(__file__).resolve().parent
NAMES = ("data-analysis", "report-writing", "presentation", "deliverable-check", "deploy-website", "browser-automation")
PREVIEW_VERSION = "1.0.1"
PREVIEW_PREVIOUS_SHA256 = ("a82a396ce0cde57329df8fdcba071684714babc00c0f403f3b32dd25df2f8436",)
BROWSER_VERSION = "1.0.0"


def write_archive(folder, archive_path):
    if not (folder / "SKILL.md").is_file():
        raise ValueError(f"skill package is missing SKILL.md: {folder}")
    with zipfile.ZipFile(archive_path, "w", compression=zipfile.ZIP_STORED) as archive:
        for source in sorted(folder.rglob("*")):
            if source.is_symlink(): raise ValueError("skill package may not contain links")
            if not source.is_file() or "__pycache__" in source.parts: continue
            info = zipfile.ZipInfo(source.relative_to(folder).as_posix(), date_time=(1980,1,1,0,0,0))
            info.external_attr = 0o100644 << 16
            archive.writestr(info, source.read_bytes())


def package(destination, root=ROOT):
    destination.mkdir(parents=True, exist_ok=True)
    records = []
    for name in NAMES:
        folder = root / "skills" / name
        version = {"deploy-website": PREVIEW_VERSION, "browser-automation": BROWSER_VERSION}.get(name, "1.0.3")
        archive_path = destination / (name + "-" + version + ".zip")
        write_archive(folder, archive_path)
        with archive_path.open("rb") as stream: digest = hashlib.file_digest(stream,"sha256").hexdigest()
        records.append({"name":name,"version":version,"scope":"global","entry":"SKILL.md",
                        "archive":archive_path.name,"sha256":digest,"bytes":archive_path.stat().st_size})
    manifest = {"schema":"jingjiaagent.official.skills.v1","skills":records}
    (destination / "manifest.json").write_text(json.dumps(manifest,ensure_ascii=False,indent=2),encoding="utf-8")
    return manifest


def package_backend_preview(destination):
    """Update only the preview package; never rewrite existing office archives."""
    manifest_path = destination / 'manifest.json'
    manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
    archive_path = destination / 'deploy-website.zip'
    write_archive(ROOT / 'skills' / 'deploy-website', archive_path)
    record = dict(id='deploy-website', name='deploy-website', version=PREVIEW_VERSION,
                  sha256=hashlib.sha256(archive_path.read_bytes()).hexdigest(),
                  description='启动工作空间网页服务并获取域名预览地址',
                  previous_sha256=list(PREVIEW_PREVIOUS_SHA256))
    manifest['packages'] = [p for p in manifest['packages'] if p['id'] != record['id']] + [record]
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def package_backend_browser(destination):
    """Embed the independent browser skill without modifying other releases."""
    manifest_path = destination / 'manifest.json'
    manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
    archive_path = destination / 'browser-automation.zip'
    write_archive(ROOT / 'skills' / 'browser-automation', archive_path)
    record = dict(id='browser-automation', name='browser-automation', version=BROWSER_VERSION,
                  sha256=hashlib.sha256(archive_path.read_bytes()).hexdigest(),
                  description='使用任务内浏览器读取网页、交互、截图和检查，无需开放预览地址')
    manifest['packages'] = [p for p in manifest['packages'] if p['id'] != record['id']] + [record]
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


if __name__ == "__main__":
    parser = argparse.ArgumentParser(); parser.add_argument("--output",type=Path,default=ROOT/".build/work-skills")
    parser.add_argument('--backend-output', type=Path)
    args = parser.parse_args()
    package(args.output)
    if args.backend_output:
        package_backend_preview(args.backend_output)
        package_backend_browser(args.backend_output)
