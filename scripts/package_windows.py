import hashlib
import pathlib
import shutil
import zipfile

root = pathlib.Path(__file__).resolve().parents[1]
source = root / "bin/stacker.exe"
if not source.is_file():
    raise SystemExit(f"Build the Windows executable first: {source}")
destination = root / "dist/windows"
destination.mkdir(parents=True, exist_ok=True)
asset = destination / "stacker-windows-amd64.exe"
shutil.copy2(source, asset)
shutil.copy2(root / "install.ps1", destination / "install.ps1")
digest = hashlib.sha256(asset.read_bytes()).hexdigest()
(destination / "checksums.txt").write_text(f"{digest}  {asset.name}\n", encoding="utf-8")
archive = destination.parent / "stacker-windows-amd64.zip"
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
    for path in (asset, destination / "checksums.txt", destination / "install.ps1"):
        bundle.write(path, path.name)
print(archive)
