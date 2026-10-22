"""Validate a bounded local output tree and produce an integrity manifest."""
import argparse
import hashlib
import json
import mimetypes
from pathlib import Path
import stat
import zipfile


def check(root):
    if root.is_symlink() or not root.is_dir(): raise ValueError("plain output directory required")
    result = []; total = 0
    required = {".docx":"word/document.xml", ".xlsx":"xl/workbook.xml", ".pptx":"ppt/presentation.xml"}
    for file in sorted(root.rglob("*")):
        mode = file.lstat().st_mode
        if stat.S_ISLNK(mode) or not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)): raise ValueError("links and special files are forbidden")
        if not file.is_file(): continue
        size = file.stat().st_size; total += size
        if size > 100 << 20 or total > 256 << 20 or len(result) >= 2000: raise ValueError("output exceeds checkpoint limits")
        status = "needs_review"
        if file.suffix.lower() in required:
            with zipfile.ZipFile(file) as archive:
                names = archive.namelist()
                if len(names) != len(set(names)) or "[Content_Types].xml" not in names or required[file.suffix.lower()] not in names or archive.testzip(): raise ValueError("invalid Office document")
        elif file.suffix.lower() == ".pdf":
            with file.open("rb") as stream:
                if stream.read(5) != b"%PDF-": raise ValueError("invalid PDF")
        with file.open("rb") as stream: digest = hashlib.file_digest(stream, "sha256").hexdigest()
        result.append({"path":file.relative_to(root).as_posix(), "size":size, "sha256":digest,
                       "mime_type":mimetypes.guess_type(file.name)[0] or "application/octet-stream", "status":status,
                       "checks":{"local_structure":True,"server_render":False}})
    return {"schema":"jingjiaagent.deliverables.v1","files":result}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(); parser.add_argument("root", type=Path)
    parser.add_argument("--manifest", type=Path, default=Path(".jingjiaagent/work/deliverables.json")); args = parser.parse_args()
    manifest = check(args.root); args.manifest.parent.mkdir(parents=True, exist_ok=True)
    args.manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
