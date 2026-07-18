#!/usr/bin/env python3
"""Validate artifacts and publish a Stacker GitHub release."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


TAG_PATTERN = re.compile(
    r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
EXPECTED_BINARIES = {
    "stacker-darwin-amd64",
    "stacker-darwin-arm64",
    "stacker-linux-amd64",
    "stacker-linux-arm64",
}


class GitHubAPIError(RuntimeError):
    """An unsuccessful GitHub API response."""

    def __init__(self, status: int, body: str):
        super().__init__(f"GitHub API returned HTTP {status}: {body}")
        self.status = status


class GitHubAPI:
    """Minimal GitHub Releases API client using only the standard library."""

    def __init__(self, token: str, api_url: str):
        self.token = token
        self.api_url = api_url.rstrip("/")

    def _headers(self, content_type: str = "application/json") -> dict[str, str]:
        return {
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {self.token}",
            "Content-Type": content_type,
            "User-Agent": "stacker-release-script",
            "X-GitHub-Api-Version": "2022-11-28",
        }

    def request_json(
        self,
        method: str,
        url: str,
        payload: dict[str, Any] | None = None,
    ) -> dict[str, Any] | list[dict[str, Any]] | None:
        data = json.dumps(payload).encode() if payload is not None else None
        request = Request(
            url,
            data=data,
            headers=self._headers(),
            method=method,
        )
        try:
            with urlopen(request, timeout=30) as response:
                body = response.read()
        except HTTPError as error:
            body = error.read().decode(errors="replace")
            raise GitHubAPIError(error.code, body) from error
        except URLError as error:
            raise RuntimeError(f"GitHub API request failed: {error.reason}") from error

        if not body:
            return None
        return json.loads(body)

    def upload_asset(self, upload_url: str, asset_path: Path) -> dict[str, Any]:
        base_url = upload_url.split("{", 1)[0]
        url = f"{base_url}?{urlencode({'name': asset_path.name})}"
        request = Request(
            url,
            data=asset_path.read_bytes(),
            headers=self._headers("application/octet-stream"),
            method="POST",
        )
        try:
            with urlopen(request, timeout=120) as response:
                return json.loads(response.read())
        except HTTPError as error:
            body = error.read().decode(errors="replace")
            raise GitHubAPIError(error.code, body) from error
        except URLError as error:
            raise RuntimeError(f"Asset upload failed: {error.reason}") from error


def validate_tag(tag: str) -> str:
    """Validate and return a SemVer release tag."""
    if not TAG_PATTERN.fullmatch(tag):
        raise ValueError(
            f"invalid release tag {tag!r}; expected SemVer such as v1.2.3 or v1.2.3-rc.1"
        )
    return tag


def sha256(path: Path) -> str:
    """Calculate a file's SHA-256 checksum."""
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def collect_and_verify_assets(release_dir: Path) -> list[Path]:
    """Collect the expected release files and verify checksums."""
    if not release_dir.is_dir():
        raise FileNotFoundError(f"release directory not found: {release_dir}")

    binary_paths = {path.name: path for path in release_dir.glob("stacker-*")}
    missing = EXPECTED_BINARIES - binary_paths.keys()
    unexpected = binary_paths.keys() - EXPECTED_BINARIES
    if missing:
        raise RuntimeError(f"missing release binaries: {', '.join(sorted(missing))}")
    if unexpected:
        raise RuntimeError(f"unexpected release binaries: {', '.join(sorted(unexpected))}")

    checksums_path = release_dir / "checksums.txt"
    if not checksums_path.is_file():
        raise FileNotFoundError(f"checksum file not found: {checksums_path}")

    checksums: dict[str, str] = {}
    for line_number, line in enumerate(checksums_path.read_text().splitlines(), start=1):
        parts = line.split()
        if len(parts) != 2:
            raise RuntimeError(f"invalid checksums.txt line {line_number}: {line!r}")
        checksum, filename = parts
        checksums[filename.removeprefix("*")] = checksum.lower()

    if checksums.keys() != EXPECTED_BINARIES:
        raise RuntimeError("checksums.txt does not contain exactly the expected binaries")

    for filename, path in binary_paths.items():
        actual = sha256(path)
        if actual != checksums[filename]:
            raise RuntimeError(
                f"checksum mismatch for {filename}: expected {checksums[filename]}, got {actual}"
            )

    return [binary_paths[name] for name in sorted(EXPECTED_BINARIES)] + [
        checksums_path
    ]


def create_or_get_release(
    api: GitHubAPI,
    repository: str,
    tag: str,
    target_commitish: str,
) -> dict[str, Any]:
    """Return an existing release or create a new one."""
    releases_url = f"{api.api_url}/repos/{repository}/releases"
    tag_url = f"{releases_url}/tags/{quote(tag, safe='')}"
    try:
        existing = api.request_json("GET", tag_url)
        if not isinstance(existing, dict):
            raise RuntimeError("GitHub returned an invalid release response")
        print(f"Using existing release: {tag}")
        return existing
    except GitHubAPIError as error:
        if error.status != 404:
            raise

    prerelease = "-" in tag.partition("+")[0]
    payload = {
        "tag_name": tag,
        "target_commitish": target_commitish,
        "name": tag,
        "draft": False,
        "prerelease": prerelease,
        "generate_release_notes": True,
    }
    created = api.request_json("POST", releases_url, payload)
    if not isinstance(created, dict):
        raise RuntimeError("GitHub returned an invalid release response")
    print(f"Created release: {tag}")
    return created


def replace_assets(
    api: GitHubAPI,
    repository: str,
    release: dict[str, Any],
    assets: list[Path],
) -> None:
    """Replace assets with matching names and upload the current files."""
    expected_names = {asset.name for asset in assets}
    for existing in release.get("assets", []):
        if existing.get("name") not in expected_names:
            continue
        asset_id = existing.get("id")
        print(f"Deleting existing asset: {existing['name']}")
        api.request_json(
            "DELETE",
            f"{api.api_url}/repos/{repository}/releases/assets/{asset_id}",
        )

    upload_url = release.get("upload_url")
    if not isinstance(upload_url, str):
        raise RuntimeError("GitHub release response does not include upload_url")

    for asset in assets:
        print(f"Uploading {asset.name} ({asset.stat().st_size:,} bytes)...")
        uploaded = api.upload_asset(upload_url, asset)
        print(f"Uploaded: {uploaded.get('browser_download_url', asset.name)}")


def publish_release() -> None:
    """Validate the environment and publish all release assets."""
    repository = os.environ.get("GITHUB_REPOSITORY", "").strip()
    token = os.environ.get("GITHUB_TOKEN", "").strip()
    tag = validate_tag(
        os.environ.get("RELEASE_TAG", "").strip()
        or os.environ.get("GITHUB_REF_NAME", "").strip()
    )
    target_commitish = os.environ.get("GITHUB_SHA", "").strip() or "main"
    api_url = os.environ.get("GITHUB_API_URL", "https://api.github.com")

    if not repository:
        raise RuntimeError("GITHUB_REPOSITORY is required")
    if not token:
        raise RuntimeError("GITHUB_TOKEN is required")

    assets = collect_and_verify_assets(Path("release"))
    api = GitHubAPI(token, api_url)
    release = create_or_get_release(api, repository, tag, target_commitish)
    replace_assets(api, repository, release, assets)
    print(f"Release published successfully: {tag}")


def parse_args() -> argparse.Namespace:
    """Parse command-line arguments."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--validate-tag",
        metavar="TAG",
        help="validate a release tag without contacting GitHub",
    )
    return parser.parse_args()


def main() -> int:
    """Run the release publisher."""
    args = parse_args()
    try:
        if args.validate_tag:
            print(validate_tag(args.validate_tag))
        else:
            publish_release()
    except (GitHubAPIError, OSError, RuntimeError, ValueError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
