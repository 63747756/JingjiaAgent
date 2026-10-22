"""Build deterministic, verified offline packages of the four official skills."""
import argparse
import hashlib
import json
from pathlib import Path
import zipfile

ROOT = Path(__file__).resolve().parent
NAMES = ("data-analysis", "report-writing", "presentation", "deliverable-check")


def package(destination, root=ROOT):
    destination.mkdir(parents=True, exist_ok=True)
    records = []
    for name in NAMES:
        folder = root / "skills" / name
        archive_path = destination / (name + "-1.0.1.zip")
        with zipfile.ZipFile(archive_path, "w", compression=zipfile.ZIP_STORED) as archive:
            for source in sorted(folder.rglob("*")):
                if source.is_symlink(): raise ValueError("skill package may not contain links")
                if not source.is_file() or "__pycache__" in source.parts: continue
                info = zipfile.ZipInfo(source.relative_to(folder).as_posix(), date_time=(1980,1,1,0,0,0))
                info.external_attr = 0o100644 << 16
                archive.writestr(info, source.read_bytes())
        with archive_path.open("rb") as stream: digest = hashlib.file_digest(stream,"sha256").hexdigest()
        records.append({"name":name,"version":"1.0.1","scope":"global","entry":"SKILL.md",
                        "archive":archive_path.name,"sha256":digest,"bytes":archive_path.stat().st_size})
    manifest = {"schema":"jingjiaagent.official.skills.v1","skills":records}
    (destination / "manifest.json").write_text(json.dumps(manifest,ensure_ascii=False,indent=2),encoding="utf-8")
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(); parser.add_argument("--output",type=Path,default=ROOT/".build/work-skills")
    package(parser.parse_args().output)
