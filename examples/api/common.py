"""Shared helpers for GoWe API examples.

Configuration via environment variables:
    GOWE_URL      Base URL (default: http://localhost:8091)
    BVBRC_TOKEN   BV-BRC authentication token (optional)
"""

import json
import os
import sys
import urllib.request
import urllib.error
import urllib.parse

GOWE_URL = os.environ.get("GOWE_URL", "http://localhost:8091").rstrip("/")
BVBRC_TOKEN = os.environ.get("BVBRC_TOKEN", "")


def api(method: str, path: str, body: dict | None = None, params: dict | None = None) -> dict:
    """Make an API request and return the parsed response envelope."""
    url = f"{GOWE_URL}/api/v1{path}"
    if params:
        url += "?" + urllib.parse.urlencode(params)

    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if BVBRC_TOKEN:
        req.add_header("Authorization", BVBRC_TOKEN)

    try:
        with urllib.request.urlopen(req) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        message = e.reason
        raw = e.read()
        if raw:
            text = raw.decode("utf-8", errors="replace").strip()
            try:
                err = json.loads(text)
                if isinstance(err, dict):
                    message = err.get("error", {}).get("message", text)
            except (json.JSONDecodeError, TypeError):
                message = text
        print(f"HTTP {e.code}: {message}", file=sys.stderr)
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Request failed: unable to reach {url}: {e.reason}", file=sys.stderr)
        sys.exit(1)


def pp(obj):
    """Pretty-print a JSON-serializable object."""
    print(json.dumps(obj, indent=2))
