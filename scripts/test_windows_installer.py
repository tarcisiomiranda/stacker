import hashlib
import http.server
import pathlib
import shutil
import subprocess
import tempfile
import threading
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
ASSET = "stacker-windows-amd64.exe"
BINARY = "stacker.exe"


class WindowsInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="stacker-installer-")
        self.directory = pathlib.Path(self.temporary.name) / "space directory" / "unicode-\u00e7"
        self.payload = b"MZ-synthetic-stacker-executable"
        self.checksums = f"{hashlib.sha256(self.payload).hexdigest()}  {ASSET}\r\n".encode()
        self.requests = []
        case = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                case.requests.append(self.path)
                if self.path.endswith("/checksums.txt"):
                    body = case.checksums
                elif self.path.endswith("/" + ASSET):
                    body = case.payload
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.shell = shutil.which("powershell") or shutil.which("pwsh")
        self.assertIsNotNone(self.shell, "Windows PowerShell or PowerShell 7 is required")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join()
        self.temporary.cleanup()

    def install(self, version="latest", extra=()):
        return subprocess.run(
            [self.shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
             "-File", str(ROOT / "install.ps1"), "-InstallDir", str(self.directory),
             "-ReleaseBaseUrl", f"http://127.0.0.1:{self.server.server_port}/releases",
             "-Version", version, "-NoPath", *extra],
            capture_output=True, timeout=30,
        )

    def previous(self):
        self.directory.mkdir(parents=True)
        path = self.directory / BINARY
        path.write_bytes(b"previous-installation")
        return path

    def test_latest_installs_in_unicode_path_and_cleans_staging(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))
        self.assertEqual((self.directory / BINARY).read_bytes(), self.payload)
        self.assertIn("/releases/latest/download/" + ASSET, self.requests)
        self.assertEqual(list(self.directory.glob(".install-*")), [])

    def test_pinned_version_and_atomic_upgrade(self):
        path = self.previous()
        result = self.install("v1.2.3")
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))
        self.assertEqual(path.read_bytes(), self.payload)
        self.assertIn("/releases/download/v1.2.3/" + ASSET, self.requests)

    def test_checksum_failure_preserves_existing_executable(self):
        path = self.previous()
        self.checksums = f"{'0' * 64}  {ASSET}\n".encode()
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(path.read_bytes(), b"previous-installation")
        self.assertEqual(list(self.directory.glob(".install-*")), [])

    def test_missing_checksum_rejects_before_binary_download(self):
        self.checksums = b""
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.directory / BINARY).exists())
        self.assertFalse(any(path.endswith("/" + ASSET) for path in self.requests))

    def test_duplicate_checksum_is_rejected(self):
        self.checksums *= 2
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.directory / BINARY).exists())

    def test_invalid_version_makes_no_request(self):
        result = self.install("../main")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.requests, [])

    def test_local_package_installs_without_network(self):
        source = pathlib.Path(self.temporary.name) / "offline"
        source.mkdir()
        (source / ASSET).write_bytes(self.payload)
        (source / "checksums.txt").write_bytes(self.checksums)
        result = self.install(extra=("-SourceDir", str(source)))
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))
        self.assertEqual((self.directory / BINARY).read_bytes(), self.payload)
        self.assertEqual(self.requests, [])

    def test_locked_executable_preserves_previous_installation(self):
        path = self.previous()
        with path.open("rb"):
            result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(path.read_bytes(), b"previous-installation")
        self.assertEqual(list(self.directory.glob(".install-*")), [])


if __name__ == "__main__":
    unittest.main()
