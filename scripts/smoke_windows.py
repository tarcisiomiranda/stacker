import argparse
import json
import pathlib
import subprocess
import tempfile
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--binary", type=pathlib.Path, default=ROOT / "bin/stacker.exe")

args = parser.parse_args()
binary = args.binary.resolve()


def run(*arguments, check=True):
    result = subprocess.run([str(binary), *arguments], capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=30)
    if check and result.returncode:
        raise RuntimeError(result.stderr or result.stdout)
    return result


run("version")
with tempfile.TemporaryDirectory(prefix="stacker-smoke-") as temporary:
    config = pathlib.Path(temporary) / "stacker.yml"
    config.write_text("version: 1\nprocesses:\n  smoke:\n    command: powershell -NoProfile -NonInteractive -Command \"Write-Output 'windows-smoke'; Start-Sleep -Seconds 120\"\n    graceful_timeout: 1s\n", encoding="utf-8")
    prefix = ("--config", str(config))
    try:
        run(*prefix, "serve", "-d")
        run(*prefix, "ping")
        run(*prefix, "start", "smoke")
        deadline = time.monotonic() + 15
        while True:
            logs = run(*prefix, "logs", "smoke").stdout
            if "windows-smoke" in logs:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("supervised process produced no expected output: " + logs)
            time.sleep(0.1)
        run(*prefix, "list", "--json")
        run(*prefix, "restart", "smoke")
        run(*prefix, "stop", "smoke")
    finally:
        run(*prefix, "down", check=False)
print("stacker: Windows executable smoke passed")
