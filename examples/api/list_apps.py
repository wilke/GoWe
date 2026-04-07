#!/usr/bin/env python3
"""List available BV-BRC applications.

Usage:
    python list_apps.py
    python list_apps.py --search Assembly
    GOWE_URL=http://myhost:9090 python list_apps.py
"""

import argparse
from common import api

parser = argparse.ArgumentParser(description="List available BV-BRC applications.")
parser.add_argument("--search", help="Filter apps by ID or label")
args = parser.parse_args()
search = args.search

resp = api("GET", "/apps")
apps = resp["data"]

if search:
    search_lower = search.lower()
    apps = [a for a in apps if search_lower in a.get("id", "").lower() or search_lower in a.get("label", "").lower()]

print(f"{'ID':<40} {'Label'}")
print(f"{'─' * 40} {'─' * 40}")
for app in sorted(apps, key=lambda a: a.get("id", "")):
    app_id = app.get("id", "?")
    label = app.get("label", app.get("description", "")[:40])
    print(f"{app_id:<40} {label}")

print(f"\n{len(apps)} app(s) found")
