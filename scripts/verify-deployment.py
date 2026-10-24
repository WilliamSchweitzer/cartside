#!/usr/bin/env python3
"""Isolated production-style ARM64/HTTPS test. Never reads development .env.
Requires Python/PyYAML, Docker, openssl, Node 24, npm and Playwright Chromium.
Build images first with IMAGE_TAG=demo-001. Uses only disposable test users/data.
"""
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[1]

def main():
    project = 'cartside-deploy-test-' + secrets.token_hex(5)
    env = {k:v for k,v in os.environ.items() if not k.startswith(('COMPOSE_', 'CARTSIDE_'))}
    with tempfile.TemporaryDirectory(prefix=project) as tmp:
        folder = Path(tmp)
        folder.chmod(0o700)
        certs = folder/'certs'
        certs.mkdir(mode=0o755)
        origin = 'https://cartside.localhost:18443'
        auth = 'https://auth.cartside.localhost:18443'
        env.update(IMAGE_TAG='demo-001', POSTGRES_USER='e2e', POSTGRES_DB='e2e', KEYCLOAK_ADMIN_USERNAME='test-admin')
        for k in ('POSTGRES_PASSWORD','KEYCLOAK_DB_PASSWORD','KEYCLOAK_ADMIN_PASSWORD','CARTSIDE_OIDC_CLIENT_SECRET','CARTSIDE_SESSION_SECRET','CARTSIDE_AI_SERVICE_KEY'):
            env[k] = secrets.token_hex(32)
        def run(args, **kw):
            p = subprocess.run(args, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=300, **kw)
            if p.returncode: raise RuntimeError('Failed: '+ ' '.join(args[:3])+' (sensitive output suppressed)')
        run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(certs/'privkey.pem'),'-out',str(certs/'fullchain.pem'),'-subj','/CN=cartside.localhost','-addext','subjectAltName=DNS:cartside.localhost,DNS:auth.cartside.localhost'])
        (certs/'privkey.pem').chmod(0o644) # Disposable key; readable by nginx UID.
        realm=json.loads((ROOT/'infra/deploy/realm.json').read_text())
        users=[dict(username='test-'+secrets.token_hex(5),password=secrets.token_hex(24)) for _ in range(2)]
        realm['users']=[dict(username=u['username'],enabled=True,emailVerified=True,firstName='Test',lastName='Shopper',email=u['username']+'@example.invalid',credentials=[dict(type='password',value=u['password'],temporary=False)]) for u in users]
        (folder/'realm.json').write_text(json.dumps(realm).replace('https://cartside.wschweitzer.com',origin))
        (folder/'apisix.yaml').write_text((ROOT/'infra/deploy/apisix.yaml').read_text().replace('https://cartside.wschweitzer.com',origin))
        edge=(ROOT/'infra/deploy/edge.conf').read_text().replace('auth.cartside.wschweitzer.com','auth.cartside.localhost').replace('cartside.wschweitzer.com','cartside.localhost').replace('X-Forwarded-Port 443','X-Forwarded-Port 18443').replace('Host cartside.localhost;','Host cartside.localhost:18443;').replace('Host auth.cartside.localhost;','Host auth.cartside.localhost:18443;')
        (folder/'edge.conf').write_text(edge)
        compose=yaml.safe_load((ROOT/'compose.deploy.yaml').read_text())
        s=compose['services']
        for name,service in s.items():
            service.pop('build',None)
            service['volumes']=[str(ROOT/v[2:]) if v.startswith('./') else v for v in service.get('volumes',[])]
            service['logging']={'driver':'none'}
        s['edge']['ports']=['127.0.0.1:18443:8443']
        s['edge']['volumes']=[f'{folder}/edge.conf:/etc/nginx/conf.d/default.conf:ro',f'{certs}:/certs:ro']
        s['keycloak']['volumes'][0]=f'{folder}/realm.json:/opt/keycloak/data/import/cartside-realm.json:ro'
        s['keycloak']['environment']['KC_HOSTNAME']=auth
        s['apisix']['volumes'][1]=f'{folder}/apisix.yaml:/usr/local/apisix/conf/apisix.yaml:ro'
        s['api']['environment'].update(BROWSER_ORIGIN=origin,OIDC_ISSUER=auth+'/realms/cartside')
        config=folder/'compose.json';config.write_text(json.dumps(compose));config.chmod(0o600)
        command=['docker','compose','--env-file','/dev/null','-p',project,'-f',str(config)]
        try:
            print('Starting isolated ARM64 deployment',flush=True)
            run(command+['up','-d','--wait','--wait-timeout','240'])
            run(command+['run','--rm','--no-deps','--entrypoint','migrate','api','up'])
            run(command+['run','--rm','--no-deps','--entrypoint','seed','api'])
            private=folder/'browser.json';private.write_text(json.dumps(dict(origin=origin,users=users,compose=command,deployment=True)));private.chmod(0o600)
            env['CARTSIDE_E2E_CONFIG']=str(private)
            env['CARTSIDE_E2E_HTTPS']='1'
            p=subprocess.run(['npm','run','test:e2e'],cwd=ROOT/'frontend',env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=600)
            for line in p.stdout.splitlines():
                if line.startswith('CARTSIDE-E2E '): print(line,flush=True)
            if p.returncode: raise RuntimeError('HTTPS browser suite failed; sensitive diagnostics suppressed')
            print('HTTPS deployment suite passed',flush=True)
        finally:
            subprocess.run(command+['down','-v','--remove-orphans'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=60)

if __name__=='__main__':main()
