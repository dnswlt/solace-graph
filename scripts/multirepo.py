#!/usr/bin/env python3
"""
multirepo.py - Clone or pull all repositories in a Bitbucket Data Center project.

Usage:
    python multirepo.py https://bitbucket.example.com/projects/MY_PROJECT [--target-dir ./repos] [--token TOKEN] [--ssh] [--jobs 4]

Authentication:
    Pass a personal access token via --token or the BITBUCKET_TOKEN environment variable.
    If neither is set, the script attempts unauthenticated access (works for public projects).
"""

import argparse
import os
import subprocess
import sys
import urllib.request
import urllib.parse
import json
import threading
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
            with print_lock:
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
    # Prevent Git from ever prompting for credentials on the terminal.
    # This ensures that failed auth (e.g. missing SSH key) fails fast instead of hanging.
    env["GIT_TERMINAL_PROMPT"] = "0"
    
    if os.path.isdir(os.path.join(dest, ".git")):
        with print_lock:
            print(f"  pulling  {slug} ({dest})")
        result = subprocess.run(
            ["git", "-C", dest, "pull", "--ff-only"],
            capture_output=True, text=True, env=env
        )
    else:
        with print_lock:
            print(f"  cloning  {url} -> {dest}")
        result = subprocess.run(
            ["git", "clone", url, dest],
            capture_output=True, text=True, env=env
        )

    with print_lock:
        if result.returncode != 0:
            print(f"  FAILED: {slug}: {result.stderr.strip()}", file=sys.stderr)
            return False
        if result.stdout.strip():
            # Filter out "Already up to date" to reduce noise in parallel mode
            out = result.stdout.strip()
            if out != "Already up to date.":
                print(f"  {slug}: {out}")
    return True


def main():
    parser = argparse.ArgumentParser(description="Clone or pull all repos in a Bitbucket Data Center project.")
    parser.add_argument("project_url", help="Bitbucket project URL, e.g. https://bitbucket.example.com/projects/MY_PROJECT")
    parser.add_argument("--target-dir", default=".", help="Directory to clone repos into (default: current directory)")
    parser.add_argument("--token", default=None, help="Bitbucket personal access token (or set BITBUCKET_TOKEN env var)")
    parser.add_argument("--ssh", action="store_true", help="Prefer SSH clone URLs over HTTPS")
    parser.add_argument("--jobs", "-j", type=int, default=4, help="Number of parallel sync jobs (default: 4)")
    parser.add_argument("--include-archived", action="store_true", help="Include repositories that are archived (skipped by default)")
    args = parser.parse_args()

    token = args.token or os.environ.get("BITBUCKET_TOKEN")

    # ... (URL parsing logic remains the same) ...
    url = args.project_url.rstrip("/")
    parts = url.split("/projects/")
    if len(parts) != 2 or not parts[1]:
        print(f"Invalid project URL: {args.project_url!r}", file=sys.stderr)
        print("Expected format: https://<host>/projects/<PROJECT_KEY>", file=sys.stderr)
        sys.exit(1)

    base_url = parts[0]
    project_key = parts[1].split("/")[0]

    print(f"Fetching repositories for project {project_key!r} from {base_url} ...")
    all_repos = fetch_repos(base_url, project_key, token)
    
    # Filter archived repos unless requested otherwise
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

    failed = []
    with ThreadPoolExecutor(max_workers=args.jobs) as executor:
        results = list(executor.map(task, repos))
        failed = [r for r in results if r is not None]

    # Identify local repos that are no longer in the project (or filtered out)
    active_slugs = {repo["slug"] for repo in repos}
    obsolete = []
    if os.path.isdir(target_dir):
        for entry in os.listdir(target_dir):
            full_path = os.path.join(target_dir, entry)
            if os.path.isdir(os.path.join(full_path, ".git")):
                if entry not in active_slugs:
                    obsolete.append(entry)

    print()
    if obsolete:
        print(f"No longer in project ({len(obsolete)}):")
        for slug in sorted(obsolete):
            print(f"  {slug}")
        print()

    if failed:
        print(f"Failed ({len(failed)}): {', '.join(failed)}", file=sys.stderr)
        sys.exit(1)
    else:
        print("All repositories synced successfully.")


if __name__ == "__main__":
    main()
