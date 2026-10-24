#!/usr/bin/env python3
"""Static deployment boundary checks and optional registry ARM64 verification."""
import argparse
import json
from pathlib import Path
import subprocess
import yaml

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--manifests', action='store_true')
args = parser.parse_args()
doc = yaml.safe_load((ROOT/'compose.deploy.yaml').read_text())
services = doc['services']
for name, service in services.items():
    assert service['platform'] == 'linux/arm64', name
    assert not service.get('extra_hosts'), name
    assert name == 'edge' or not service.get('ports'), name
    assert service['logging']['options']['max-size'] == '10m', name
    assert service.get('healthcheck') or name == 'keycloak-db', name
    if name in ('prometheus','grafana'): assert service['profiles'] == ['observability']
assert services['keycloak']['command'] == ['start', '--import-realm']
assert not any('host.docker.internal' in (ROOT/p).read_text() for p in ['compose.deploy.yaml','infra/deploy/apisix.yaml','infra/deploy/prometheus.yaml'])
routes = yaml.safe_load((ROOT/'infra/deploy/apisix.yaml').read_text())
for route in routes['routes']:
    oidc = route.get('plugins',{}).get('openid-connect')
    if oidc:
        assert oidc['session']['cookie_secure'] is True
        assert oidc['ssl_verify'] and oidc['use_pkce'] and oidc['use_nonce']
        assert not oidc['accept_none_alg'] and not oidc['accept_unsupported_alg']
        assert oidc['redirect_uri'] == 'https://cartside.wschweitzer.com/auth/callback'
realm=json.loads((ROOT/'infra/deploy/realm.json').read_text())
assert 'users' not in realm
assert realm['clients'][0]['redirectUris'] == ['https://cartside.wschweitzer.com/auth/callback']
print('Deployment boundaries/configuration passed')
if args.manifests:
    images={s['image'] for s in services.values() if not s['image'].startswith('cartside-')}
    for path in ['backend/Dockerfile','frontend/Dockerfile','infra/mock-ai.Dockerfile']:
        images.update(line.split()[1] for line in (ROOT/path).read_text().splitlines() if line.startswith('FROM ') and line.split()[1] != 'scratch')
    for image in sorted(images):
        result=subprocess.run(['docker','buildx','imagetools','inspect',image,'--raw'],text=True,capture_output=True,timeout=60,check=True)
        manifest=json.loads(result.stdout)
        assert any(m.get('platform',{}).get('architecture')=='arm64' and m.get('platform',{}).get('os')=='linux' for m in manifest['manifests']), image
        print(image+': linux/arm64 available')
