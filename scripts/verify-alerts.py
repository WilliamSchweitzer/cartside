#!/usr/bin/env python3
"""Check committed monitoring config/rules with the Compose-pinned promtool.

No .env, running services, credentials, networks or volumes are used.
"""
from pathlib import Path
import subprocess

import yaml

ROOT = Path(__file__).resolve().parents[1]


def main():
    config = yaml.safe_load((ROOT / 'compose.yaml').read_text())
    image = config['services']['prometheus']['image']
    command = ['docker', 'run', '--rm', '--network', 'none', '--entrypoint', 'promtool',
               '-v', f'{ROOT}/infra/observability:/etc/prometheus:ro',
               '-w', '/etc/prometheus', image]
    for args in [('check', 'config', 'prometheus.yaml'),
                 ('check', 'rules', 'rules/cartside.yaml'),
                 ('test', 'rules', 'alerts.test.yaml')]:
        subprocess.run(command + list(args), check=True)


if __name__ == '__main__':
    main()
