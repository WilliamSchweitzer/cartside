#!/usr/bin/env python3
"""Explicit, disposable full-stack verification; never reads the developer .env."""
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def main():
    for binary in ('docker', 'go', 'node', 'npm'):
        if not shutil.which(binary):
            raise RuntimeError(f'Missing prerequisite: {binary}')
    if subprocess.check_output(['node', '--version'], text=True).split('.')[0] != 'v24':
        raise RuntimeError('Node 24 is required')
    import yaml  # scripts/requirements-ci.txt
    project = 'cartside-e2e-' + secrets.token_hex(6)
    children = []
    api_log_handle = None
    # Temp directory and every secret-bearing file are private; no artifacts survive.
    with tempfile.TemporaryDirectory(prefix=project) as tmp:
        folder = Path(tmp)
        os.chmod(folder, 0o700)
        ports = []
        for _ in range(5):
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0))
                ports.append(sock.getsockname()[1])
        gateway, kc, pg, api, vite = ports
        if len(set(ports)) != 5:
            raise RuntimeError('Port allocation collided; rerun')
        origin = f'http://localhost:{gateway}'
        issuer = f'http://localhost:{kc}/realms/cartside'
        env = {k: v for k, v in os.environ.items() if k not in ('DATABASE_URL', 'COMPOSE_FILE', 'COMPOSE_PROJECT_NAME', 'COMPOSE_PROFILES', 'CARTSIDE_TEST_DATABASE_URL')}
        for name in ('POSTGRES_PASSWORD', 'KEYCLOAK_DB_PASSWORD', 'KEYCLOAK_ADMIN_PASSWORD', 'CARTSIDE_OIDC_CLIENT_SECRET', 'CARTSIDE_SESSION_SECRET', 'CARTSIDE_AI_SERVICE_KEY'):
            env[name] = secrets.token_hex(32)
        env.update(POSTGRES_USER='e2e', POSTGRES_DB='e2e', KEYCLOAK_ADMIN_USERNAME='e2e-admin')
        realm = json.loads((ROOT / 'infra/keycloak/cartside-realm.json').read_text())
        users = [{'username': 'e2e-' + secrets.token_hex(5), 'password': secrets.token_hex(24)} for _ in range(2)]
        realm['users'] = [dict(username=u['username'], enabled=True, emailVerified=True, firstName='Disposable', lastName='Shopper', email=u['username']+'@example.invalid', credentials=[dict(type='password', value=u['password'], temporary=False)]) for u in users]
        client = realm['clients'][0]
        client['redirectUris'] = [origin + '/auth/callback']
        client['attributes']['post.logout.redirect.uris'] = origin + '/signed-out'
        (folder / 'realm.json').write_text(json.dumps(realm))
        routes = (ROOT / 'infra/apisix/apisix.yaml').read_text().replace('http://localhost:9080', origin).replace('host.docker.internal:8080', f'host.docker.internal:{api}').replace('host.docker.internal:5173', f'host.docker.internal:{vite}')
        (folder / 'apisix.yaml').write_text(routes)
        compose = yaml.safe_load((ROOT / 'compose.yaml').read_text())
        for name, service in compose['services'].items():
            if name not in ('apisix', 'mock-ai'):
                service['logging'] = {'driver': 'none'}
            service['volumes'] = [str(ROOT / volume[2:]) if volume.startswith('./') else volume for volume in service.get('volumes', [])]
        services = compose['services']
        services['mock-ai']['build']['context'] = str(ROOT)
        services['mock-ai']['environment'] = {'MOCK_TEST_MODE': '1'}
        services['postgres']['ports'] = [f'127.0.0.1:{pg}:5432']
        services['keycloak']['ports'] = [f'127.0.0.1:{kc}:8080']
        services['keycloak']['environment']['KC_HOSTNAME'] = f'http://localhost:{kc}'
        services['keycloak']['volumes'][0] = f'{folder}/realm.json:/opt/keycloak/data/import/cartside-realm.json:ro'
        services['apisix']['ports'] = [f'127.0.0.1:{gateway}:9080']
        services['apisix']['volumes'][1] = f'{folder}/apisix.yaml:/usr/local/apisix/conf/apisix.yaml:ro'
        config = folder / 'compose.json'
        config.write_text(json.dumps(compose))
        for private_file in folder.iterdir():
            private_file.chmod(0o600)
        command = ['docker', 'compose', '--env-file', '/dev/null', '-p', project, '-f', str(config)]
        env.update(CARTSIDE_ASSISTANT_ENABLED='true', CARTSIDE_ASSISTANT_REQUESTS_PER_MINUTE='10', CARTSIDE_ASSISTANT_MAX_USERS='1000', CARTSIDE_AI_GATEWAY_URL=origin+'/internal/ai/chat/completions', DATABASE_URL=f"postgres://e2e:{env['POSTGRES_PASSWORD']}@127.0.0.1:{pg}/e2e?sslmode=disable", HTTP_ADDR=f'0.0.0.0:{api}', BROWSER_ORIGIN=origin, OIDC_ISSUER=issuer, OIDC_AUDIENCE='cartside-api', OIDC_JWKS_URL=issuer+'/protocol/openid-connect/certs')
        def run(args, cwd=ROOT):
            result = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if result.returncode:
                raise RuntimeError('Step failed: ' + ' '.join(args[:3]) + ' (output suppressed to protect sessions/secrets)')
        try:
            print('Starting isolated infrastructure', flush=True)
            run(command + ['up', '-d', '--wait', '--wait-timeout', '180'])
            for package, test in [('catalogseed', 'TestCatalogLifecycle'), ('catalog', 'TestPostgresCatalog'), ('cart', 'TestPostgresCart'), ('orders', 'TestPostgresCheckout'), ('httpapi', 'TestPostgresCheckoutHTTP'), ('shoppingassistant', 'TestPostgresAssistant')]:
                database = 'test_' + package
                run(command + ['exec', '-T', 'postgres', 'createdb', '-U', 'e2e', database])
                env['CARTSIDE_TEST_DATABASE_URL'] = env['DATABASE_URL'].replace('/e2e?', '/'+database+'?')
                run(['go', 'test', '-tags=integration', '-race', './internal/'+package, '-run', '^'+test+'$', '-count=1'], ROOT/'backend')
                print(package + ' database suite passed', flush=True)
            run(['go', 'run', './cmd/migrate', 'up'], ROOT/'backend')
            run(['go', 'run', './cmd/seed'], ROOT/'backend')
            run(['go', 'build', '-o', str(folder/'api'), './cmd/api'], ROOT/'backend')
            api_logs = folder/'api.jsonl'
            api_log_handle = api_logs.open('w')
            api_logs.chmod(0o600)
            for args, cwd in [([str(folder/'api')], ROOT/'backend'), (['npm', 'run', 'dev', '--', '--port', str(vite)], ROOT/'frontend')]:
                children.append(subprocess.Popen(args, cwd=cwd, env=env, stdout=subprocess.DEVNULL, stderr=api_log_handle if cwd == ROOT/'backend' else subprocess.DEVNULL, start_new_session=True))
            deadline = time.monotonic() + 60
            while True:
                if any(child.poll() is not None for child in children):
                    raise RuntimeError('Host server failed; port may have been claimed. Rerun; no existing service was stopped.')
                try:
                    with urllib.request.urlopen(origin+'/api/health/ready', timeout=2) as response:
                        ready = response.status == 200
                    with urllib.request.urlopen(f'http://127.0.0.1:{vite}', timeout=2) as response:
                        ready = ready and response.status == 200
                    if ready:
                        break
                except Exception:
                    pass
                if time.monotonic() > deadline:
                    raise RuntimeError('Host server readiness timeout')
                time.sleep(0.5)
            private = folder/'browser.json'
            private.write_text(json.dumps(dict(origin=origin, users=users, compose=command)))
            private.chmod(0o600)
            env['CARTSIDE_E2E_CONFIG'] = str(private)
            env.pop('DEBUG', None)
            env.pop('PWDEBUG', None)
            print('Running Chromium shopping suite (artifacts disabled)', flush=True)
            browser = subprocess.run(['npm', 'run', 'test:e2e'], cwd=ROOT/'frontend', env=env,
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            for line in browser.stdout.splitlines():
                if line.startswith('CARTSIDE-E2E '):
                    print(line, flush=True)
            if browser.returncode:
                raise RuntimeError('Browser suite failed (sensitive diagnostics suppressed)')
            stats = subprocess.check_output(command + ['exec', '-T', 'mock-ai', '/mock-ai', '-probe', 'http://127.0.0.1:8090/test/stats'], env=env, stderr=subprocess.DEVNULL)
            if json.loads(stats)['calls'] != 2:
                raise RuntimeError('Unexpected provider invocation count')
            time.sleep(3.2)  # Flush buffered gateway access events.
            gateway_logs = subprocess.check_output(command + ['logs', '--no-log-prefix', 'apisix', 'mock-ai'],
                                                   env=env, stderr=subprocess.DEVNULL, text=True)
            api_text = api_logs.read_text()
            def events(text):
                result = []
                for line in text.splitlines():
                    try:
                        result.append(json.loads(line))
                    except json.JSONDecodeError:
                        pass
                return result
            api_events, infrastructure_events = events(api_text), events(gateway_logs)
            outbound = [e for e in api_events if e.get('msg') == 'assistant_call_completed']
            if len(outbound) != 2:
                raise RuntimeError('Expected two correlated outbound assistant events')
            for call in outbound:
                request_id = call['request_id']
                matched = [e for e in infrastructure_events if e.get('request_id') == request_id]
                if (call.get('outcome') != 'success' or
                    sum(e.get('event') == 'gateway_request_completed' for e in matched) != 2 or
                    sum(e.get('component') == 'mock' for e in matched) != 1 or
                    not any(e.get('request_id') == request_id and e.get('route') == '/assistant/messages' for e in api_events)):
                    raise RuntimeError('Authenticated assistant correlation mismatch')
            if not any(e.get('route') == '/products' and e.get('status') == 200 and
                       any(g.get('request_id') == e.get('request_id') for g in infrastructure_events)
                       for e in api_events):
                raise RuntimeError('Ordinary catalog request correlation mismatch')
            sensitive = [env[name] for name in ('POSTGRES_PASSWORD', 'CARTSIDE_AI_SERVICE_KEY',
                         'CARTSIDE_OIDC_CLIENT_SECRET', 'CARTSIDE_SESSION_SECRET')]
            sensitive += [u[field] for u in users for field in ('username', 'password')]
            sensitive += ['breakfast for two', 'vegetarian dinner', 'snacks for a picnic',
                          'Bearer cartside-local-fake-provider-only']
            if any(value in api_text + gateway_logs for value in sensitive):
                raise RuntimeError('Sensitive fixture appeared in application/infrastructure logs')
            print('Authenticated browser/catalog/assistant correlation and log redaction passed', flush=True)
            api_log_handle.close()
            print('Browser suite passed; public assistant traversed both APISIX paths, internal session-only request denied', flush=True)
        finally:
            for child in children:
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGTERM)
                    try:
                        child.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(child.pid, signal.SIGKILL)
                        child.wait()
            if api_log_handle is not None:
                api_log_handle.close()
            # Only this randomly named project's volumes are removed.
            subprocess.run(command + ['down', '-v', '--remove-orphans'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, ImportError) as error:
        sys.exit(str(error))
    except KeyboardInterrupt:
        sys.exit('Interrupted; isolated resources cleaned up')
