"""Parse tracked JSON/YAML; this does not validate APISIX or OIDC behavior."""

import json
from pathlib import Path
import subprocess
import sys

import yaml

paths = subprocess.check_output(["git", "ls-files", "-z"]).decode().split("\0")
failed = False
for name in paths:
    path = Path(name)
    try:
        if path.suffix == ".json":
            json.loads(path.read_text())
        elif path.suffix in {".yaml", ".yml"}:
            list(yaml.safe_load_all(path.read_text()))
    except (ValueError, yaml.YAMLError, OSError):
        # Do not echo configuration contents, which may contain sensitive values.
        print(f"Invalid configuration syntax: {name}", file=sys.stderr)
        failed = True
sys.exit(1 if failed else 0)
