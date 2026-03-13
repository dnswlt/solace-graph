#!/usr/bin/env python3
"""
multirepo.py - Manage multiple repositories in a Bitbucket Data Center project.

This script provides two main commands:
1. sync: Clones or pulls all repositories from a specific Bitbucket project.
2. checkout-tags: Locally checks out the latest semver tag for all repositories in a directory.

Usage Examples:
    # Sync all repos from a project using a Personal Access Token:
    python multirepo.py sync https://bitbucket.example.com/projects/MY_PROJ --token MY_TOKEN --target-dir ./repos

    # Checkout the latest semver tags (e.g. v1.2.3) for all repos in a directory:
    python multirepo.py checkout-tags --target-dir ./repos

Authentication:
    For 'sync', pass a personal access token via --token or the BITBUCKET_TOKEN environment variable.
    The script will fail fast (GIT_TERMINAL_PROMPT=0) if authentication fails to prevent hangs during parallel execution.
"""

import argparse
import os
import subprocess
import sys
import urllib.request
import urllib.parse
import json
import threading
import re
from concurrent.futures import ThreadPoolExecutor

# Lock to prevent interleaved output from multiple threads
print_lock = threading.Lock()


def fetch_repos(base_url: str, project_key: str, token: str | None) -> list[dict]:
    """Fetch all repositories for a project, handling pagination."""
    repos = []
    start = 0
    limit = 100

    while True:
        params = urllib.parse.urlencode({"start": start, "limit": limit})
        url = f"{base_url}/rest/api/1.0/projects/{project_key}/repos?{params}"

        request = urllib.request.Request(url)
        if token:
            request.add_header("Authorization", f"Bearer {token}")
        request.add_header("Accept", "application/json")

        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                data = json.loads(response.read().decode())
        except urllib.error.HTTPError as e:
            body = e.read().decode(errors="replace")
            print(f"Error fetching repos: HTTP {e.code} {e.reason}", file=sys.stderr)
            if body:
                print(body, file=sys.stderr)
            sys.exit(1)

        repos.extend(data.get("values", []))

        if data.get("isLastPage", True):
            break
        start = data["nextPageStart"]

    return repos


def clone_url(repo: dict, prefer_ssh: bool) -> str | None:
    """Extract the preferred clone URL from a repo object."""
    clone_links = repo.get("links", {}).get("clone", [])
    preferred_proto = "ssh" if prefer_ssh else "http"
    fallback_proto = "http" if prefer_ssh else "ssh"

    by_name = {link["name"]: link["href"] for link in clone_links}
    return by_name.get(preferred_proto) or by_name.get(fallback_proto)


def sync_repo(url: str, dest: str) -> bool:
    """Clone the repo if dest doesn't exist, otherwise pull. Returns True on success."""
    slug = os.path.basename(dest)
    env = os.environ.copy()
    env["GIT_TERMINAL_PROMPT"] = "0"
    
    if os.path.isdir(os.path.join(dest, ".git")):
        with print_lock:
            print(f"  pulling  {slug}")
        result = subprocess.run(
            ["git", "-C", dest, "pull", "--ff-only"],
            capture_output=True, text=True, env=env
        )
    else:
        with print_lock:
            print(f"  cloning  {slug}")
        result = subprocess.run(
            ["git", "clone", url, dest],
            capture_output=True, text=True, env=env
        )

    with print_lock:
        if result.returncode != 0:
            print(f"  FAILED: {slug}: {result.stderr.strip()}", file=sys.stderr)
            return False
        if result.stdout.strip():
            out = result.stdout.strip()
            if out != "Already up to date.":
                print(f"  {slug}: {out}")
    return True


def get_latest_tag(dest: str) -> str | None:
    """Find the latest semver-compliant tag in the repository."""
    result = subprocess.run(
        ["git", "-C", dest, "tag", "-l"],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        return None

    pattern = re.compile(r'^v?(\d+)\.(\d+)\.(\d+)$')
    found_tags = []
    for line in result.stdout.splitlines():
        tag = line.strip()
        match = pattern.match(tag)
        if match:
            ver_tuple = tuple(map(int, match.groups()))
            found_tags.append((ver_tuple, tag))
    
    if not found_tags:
        return None
    
    found_tags.sort()
    return found_tags[-1][1]


def checkout_tag(dest: str, tag: str) -> bool:
    """Checkout a specific tag. Returns True on success."""
    slug = os.path.basename(dest)
    print(f"  {slug}: checking out {tag}")
    result = subprocess.run(
        ["git", "-C", dest, "checkout", tag, "--quiet"],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        print(f"  FAILED: {slug}: checkout {tag}: {result.stderr.strip()}", file=sys.stderr)
        return False
    return True


def cmd_sync(args):
    token = args.token or os.environ.get("BITBUCKET_TOKEN")
    url = args.project_url.rstrip("/")
    parts = url.split("/projects/")
    if len(parts) != 2 or not parts[1]:
        print(f"Invalid project URL: {args.project_url!r}", file=sys.stderr)
        sys.exit(1)

    base_url = parts[0]
    project_key = parts[1].split("/")[0]

    print(f"Fetching repositories for project {project_key!r} from {base_url} ...")
    all_repos = fetch_repos(base_url, project_key, token)
    
    if not args.include_archived:
        repos = [r for r in all_repos if not r.get("archived")]
        archived_count = len(all_repos) - len(repos)
        if archived_count > 0:
            print(f"Found {len(all_repos)} repositories (skipping {archived_count} archived).")
        else:
            print(f"Found {len(all_repos)} repositories.")
    else:
        repos = all_repos
        print(f"Found {len(repos)} repositories (including archived).\n")

    target_dir = os.path.abspath(args.target_dir)
    os.makedirs(target_dir, exist_ok=True)

    def task(repo):
        slug = repo["slug"]
        url = clone_url(repo, prefer_ssh=args.ssh)
        if not url:
            with print_lock:
                print(f"  SKIP {slug}: no clone URL found", file=sys.stderr)
            return None
        dest = os.path.join(target_dir, slug)
        if not sync_repo(url, dest):
            return slug
        return None

    with ThreadPoolExecutor(max_workers=args.jobs) as executor:
        results = list(executor.map(task, repos))
        failed = [r for r in results if r is not None]

    active_slugs = {repo["slug"] for repo in repos}
    obsolete = []
    if os.path.isdir(target_dir):
        for entry in os.listdir(target_dir):
            full_path = os.path.join(target_dir, entry)
            if os.path.isdir(os.path.join(full_path, ".git")):
                if entry not in active_slugs:
                    obsolete.append(entry)

    if obsolete:
        print(f"\nNo longer in project ({len(obsolete)}):")
        for slug in sorted(obsolete):
            print(f"  {slug}")

    if failed:
        print(f"\nFailed ({len(failed)}): {', '.join(failed)}", file=sys.stderr)
        sys.exit(1)
    else:
        print("\nAll repositories synced successfully.")


def cmd_checkout_tags(args):
    target_dir = os.path.abspath(args.target_dir)
    if not os.path.isdir(target_dir):
        print(f"Error: target directory {target_dir!r} does not exist.", file=sys.stderr)
        sys.exit(1)

    print(f"Scanning {target_dir} for repositories and checking out latest semver tags...")
    
    count = 0
    failed = []
    for entry in sorted(os.listdir(target_dir)):
        dest = os.path.join(target_dir, entry)
        if not os.path.isdir(os.path.join(dest, ".git")):
            continue
        
        count += 1
        tag = get_latest_tag(dest)
        if not tag:
            print(f"  {entry}: no semver tags found")
            continue
        
        if not checkout_tag(dest, tag):
            failed.append(entry)

    print(f"\nProcessed {count} repositories.")
    if failed:
        print(f"Failed ({len(failed)}): {', '.join(failed)}", file=sys.stderr)
        sys.exit(1)


def main():
    parser = argparse.ArgumentParser(description="Manage multiple repos in a Bitbucket project.")
    subparsers = parser.add_subparsers(dest="command", required=True)

    # Sync command
    parser_sync = subparsers.add_parser("sync", help="Clone or pull all repos in a project.")
    parser_sync.add_argument("project_url", help="Bitbucket project URL")
    parser_sync.add_argument("--target-dir", default=".", help="Directory to clone repos into")
    parser_sync.add_argument("--token", default=None, help="Bitbucket access token")
    parser_sync.add_argument("--ssh", action="store_true", help="Prefer SSH URLs")
    parser_sync.add_argument("--jobs", "-j", type=int, default=4, help="Parallel sync jobs")
    parser_sync.add_argument("--include-archived", action="store_true", help="Include archived repos")
    parser_sync.set_defaults(func=cmd_sync)

    # Checkout-tags command
    parser_tags = subparsers.add_parser("checkout-tags", help="Locally checkout latest semver tags.")
    parser_tags.add_argument("--target-dir", default=".", help="Directory containing the repos")
    parser_tags.set_defaults(func=cmd_checkout_tags)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
