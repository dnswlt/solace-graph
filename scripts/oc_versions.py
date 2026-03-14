#!/usr/bin/env python3
"""
oc_versions.py - Fetch currently deployed image versions from OpenShift namespaces.

Queries deployments and cronjobs across one or more namespaces and extracts the
container image tag from .spec.template.spec.containers[].image. The result is
written as a JSON file mapping resource name to image tag. This file can then be
passed to multirepo.py checkout-tags --versions-file to check out the exact
versions that are running in OpenShift.

Usage:
    python oc_versions.py <namespace> [<namespace> ...] [--output versions.json]
"""

import argparse
import json
import re
import subprocess
import sys

# "7.75.1-65df3ef-SNAPSHOT" -> version="7.75.1", git_hash="65df3ef"
_SNAPSHOT_PATTERN = re.compile(r'^(v?\d+\.\d+\.\d+)-([0-9a-f]{7,})-SNAPSHOT$')
# "7.75.1" or "v2.0.0" (release tag, no suffix)
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


def fetch_oc_versions(namespaces: list[str]) -> dict[str, dict]:
    """Return {name: {image_tag, version, git_ref, kind}} by querying deployments and cronjobs."""
    versions: dict[str, dict] = {}
    for ns in namespaces:
        print(f"  querying namespace {ns} ...")
        result = subprocess.run(
            ["oc", "get", "deployments,cronjobs", "-n", ns, "-o", "json"],
            capture_output=True, text=True
        )
        if result.returncode != 0:
            print(f"  FAILED {ns}: {result.stderr.strip()}", file=sys.stderr)
            continue
        try:
            data = json.loads(result.stdout)
        except json.JSONDecodeError as e:
            print(f"  FAILED {ns}: could not parse oc output: {e}", file=sys.stderr)
            continue
        for item in data.get("items", []):
            name = item["metadata"]["name"]
            containers = (
                item.get("spec", {})
                    .get("template", {})
                    .get("spec", {})
                    .get("containers", [])
            )
            for container in containers:
                image = container.get("image", "")
                if ":" not in image:
                    continue
                raw_tag = image.split(":")[-1]
                parsed = extract_version(raw_tag)
                if not parsed:
                    print(f"  WARNING: {name}: unrecognised image tag {raw_tag!r}, skipping", file=sys.stderr)
                    continue
                ref, kind = parsed
                version = _SNAPSHOT_PATTERN.match(raw_tag)
                entry = {
                    "image_tag": raw_tag,
                    "version": version.group(1) if version else ref,
                    "git_ref": ref,
                }
                print(f"  {name}: {raw_tag!r} -> {kind} {ref!r}")
                if name in versions and versions[name]["git_ref"] != ref:
                    print(
                        f"  WARNING: {name} already seen with ref {versions[name]['git_ref']!r},"
                        f" overwriting with {ref!r} (from namespace {ns})",
                        file=sys.stderr
                    )
                versions[name] = entry
                break  # first container wins
    return versions


def main():
    parser = argparse.ArgumentParser(
        description="Fetch deployed image versions from OpenShift namespaces."
    )
    parser.add_argument("namespaces", nargs="+", help="OpenShift namespaces to query")
    parser.add_argument(
        "--output", "-o", default="versions.json",
        help="Output JSON file (default: versions.json)"
    )
    args = parser.parse_args()

    print(f"Fetching image versions from {len(args.namespaces)} namespace(s)...")
    versions = fetch_oc_versions(args.namespaces)

    if not versions:
        print("No image versions found.", file=sys.stderr)
        sys.exit(1)

    with open(args.output, "w") as f:
        json.dump(versions, f, indent=2, sort_keys=True)
        f.write("\n")

    print(f"\nFound {len(versions)} resource(s). Written to {args.output!r}.")


if __name__ == "__main__":
    main()
