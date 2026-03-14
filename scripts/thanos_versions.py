#!/usr/bin/env python3
"""
thanos_versions.py - Fetch currently running image versions from Thanos/Prometheus.

Runs an instant query against the Thanos HTTP API and extracts the image label
from the returned time series. Image label values have the same format as
container image references (registry/path/name:tag), and tags are parsed
identically to oc_versions.py.

The result is written as a JSON file in the same format as oc_versions.py,
so it can be passed to multirepo.py checkout-tags --versions-file.

Usage:
    python thanos_versions.py <thanos-url> [--output versions.json] [--token TOKEN]

Authentication:
    Pass a bearer token via --token or the THANOS_TOKEN environment variable.
"""

import argparse
import json
import os
import re
import sys
import urllib.parse
import urllib.request

# ---------------------------------------------------------------------------
# Adjust filters here as needed — not exposed as CLI flags intentionally.
# ---------------------------------------------------------------------------
QUERY = """
sum by (image) (
  rate(container_cpu_usage_seconds_total{
    container!="POD",
    container!=""
  }[5m])
)
"""
# ---------------------------------------------------------------------------

# Reuse the same tag-parsing logic as oc_versions.py
_SNAPSHOT_PATTERN = re.compile(r'^(v?\d+\.\d+\.\d+)-([0-9a-f]{7,})-SNAPSHOT$')
_RELEASE_PATTERN = re.compile(r'^(v?\d+\.\d+\.\d+)$')


def extract_version(image_tag: str) -> tuple[str, str] | None:
    """Extract a git ref from an image tag.

    Returns (ref, kind) where kind is 'hash' or 'tag', or None if unrecognised.
    For SNAPSHOT tags (e.g. "7.75.1-65df3ef-SNAPSHOT") returns the git hash.
    For release tags (e.g. "7.75.1") returns the semver tag as-is.
    """
    m = _SNAPSHOT_PATTERN.match(image_tag)
    if m:
        return m.group(2), "hash"
    m = _RELEASE_PATTERN.match(image_tag)
    if m:
        return m.group(1), "tag"
    return None


def image_name(image: str) -> str:
    """Extract the bare image name from a full reference.

    e.g. "registry.example.com/org/my-service:1.0.0" -> "my-service"
    """
    return image.split(":")[0].rsplit("/", 1)[-1]


def fetch_thanos_versions(base_url: str, token: str | None) -> dict[str, dict]:
    """Run an instant query and return {name: {image_tag, version, git_ref}}."""
    url = f"{base_url.rstrip('/')}/api/v1/query?" + urllib.parse.urlencode({"query": QUERY})
    request = urllib.request.Request(url)
    if token:
        request.add_header("Authorization", f"Bearer {token}")
    request.add_header("Accept", "application/json")

    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            data = json.loads(response.read().decode())
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        print(f"HTTP {e.code} {e.reason}", file=sys.stderr)
        if body:
            print(body, file=sys.stderr)
        sys.exit(1)

    if data.get("status") != "success":
        print(f"Query failed: {data.get('error', 'unknown error')}", file=sys.stderr)
        sys.exit(1)

    versions: dict[str, dict] = {}
    for series in data["data"]["result"]:
        image = series["metric"].get("image", "")
        if not image or ":" not in image:
            print(f"  WARNING: series has no usable image label: {series['metric']!r}, skipping",
                  file=sys.stderr)
            continue

        raw_tag = image.split(":")[-1]
        parsed = extract_version(raw_tag)
        if not parsed:
            print(f"  WARNING: unrecognised image tag {raw_tag!r} in {image!r}, skipping",
                  file=sys.stderr)
            continue

        ref, kind = parsed
        name = image_name(image)
        version_str = _SNAPSHOT_PATTERN.match(raw_tag)
        entry = {
            "image_tag": raw_tag,
            "version": version_str.group(1) if version_str else ref,
            "git_ref": ref,
        }
        print(f"  {name}: {raw_tag!r} -> {kind} {ref!r}")
        if name in versions and versions[name]["git_ref"] != ref:
            print(
                f"  WARNING: {name} already seen with ref {versions[name]['git_ref']!r},"
                f" overwriting with {ref!r}",
                file=sys.stderr
            )
        versions[name] = entry

    return versions


def main():
    parser = argparse.ArgumentParser(
        description="Fetch running image versions from Thanos/Prometheus."
    )
    parser.add_argument("url", help="Thanos base URL (e.g. https://thanos.example.com)")
    parser.add_argument(
        "--output", "-o", default="versions.json",
        help="Output JSON file (default: versions.json)"
    )
    parser.add_argument("--token", default=None, help="Bearer token for authentication")
    args = parser.parse_args()

    token = args.token or os.environ.get("THANOS_TOKEN")

    print(f"Querying Thanos at {args.url} ...")
    versions = fetch_thanos_versions(args.url, token)

    if not versions:
        print("No image versions found.", file=sys.stderr)
        sys.exit(1)

    with open(args.output, "w") as f:
        json.dump(versions, f, indent=2, sort_keys=True)
        f.write("\n")

    print(f"\nFound {len(versions)} image(s). Written to {args.output!r}.")


if __name__ == "__main__":
    main()
