import argparse
import os
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--output")
parser.add_argument("--version")
args = parser.parse_args()
target = subprocess.check_output(["go", "env", "GOOS"], cwd=root, text=True).strip()
output = pathlib.Path(args.output) if args.output else pathlib.Path("bin") / ("stacker.exe" if target == "windows" else "stacker")
if not output.is_absolute():
    output = root / output
output.parent.mkdir(parents=True, exist_ok=True)
version = args.version or os.environ.get("RELEASE_TAG")
if not version:
    result = subprocess.run(["git", "describe", "--tags", "--always", "--dirty"], cwd=root, capture_output=True, text=True)
    version = result.stdout.strip() if result.returncode == 0 else "dev"
subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}", "-o", str(output), "./cmd/stacker"], cwd=root, check=True)
print(output)
