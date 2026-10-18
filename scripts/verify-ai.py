#!/usr/bin/env python3
"""Disposable AI gateway verification. Never reads .env or uses developer volumes."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

import yaml

ROOT = Path(__file__).resolve().parents[1]


def main():
    project = 'cartside-ai-' + secrets.token_hex(6)
    env = {k: v for k, v in os.environ.items() if not k.startswith('COMPOSE_')}
    key = secrets.token_hex(32)
    env.update(CARTSIDE_AI_SERVICE_KEY=key, CARTSIDE_OIDC_CLIENT_SECRET=secrets.token_hex(32), CARTSIDE_SESSION_SECRET=secrets.token_hex(32))
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix=project) as tmp:
        folder = Path(tmp)
        compose = yaml.safe_load((ROOT/'compose.yaml').read_text())
        compose['services'] = {k: v for k, v in compose['services'].items() if k in ('apisix', 'mock-ai')}
        compose.pop('volumes', None)
        services = compose['services']
        services['apisix']['ports'] = [f'127.0.0.1:{port}:9080']
        services['apisix']['volumes'] = [str(ROOT/v[2:]) for v in services['apisix']['volumes']]
        services['mock-ai']['build']['context'] = str(ROOT)
        services['mock-ai']['environment'] = dict(MOCK_TEST_MODE='1', MOCK_SCENARIO='normal')
        for service in services.values():
            service['logging'] = {'driver': 'none'}
        config = folder/'compose.json'
        config.write_text(json.dumps(compose)); config.chmod(0o600)
        command = ['docker', 'compose', '--env-file', '/dev/null', '-p', project, '-f', str(config)]

        def run(args, capture=False):
            result = subprocess.run(command+args, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            if result.returncode:
                # Never include secret-bearing configuration or payloads in failure output.
                raise RuntimeError('Compose step failed: ' + ' '.join(args[:2]))
            return result.stdout if capture else None

        def calls():
            return json.loads(run(['exec', '-T', 'mock-ai', '/mock-ai', '-probe', 'http://127.0.0.1:8090/test/stats'], True))['calls']

        body = dict(model='cartside-mock-v1', messages=[dict(role='user', content='breakfast for two')], stream=False)

        def request(payload=body, credential=key, path='/internal/ai/chat/completions', method='POST', cookie=None):
            headers = {'Content-Type': 'application/json'}
            if credential is not None:
                headers['X-Cartside-AI-Key'] = credential
            if cookie:
                headers['Cookie'] = cookie
            data = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
            req = urllib.request.Request(f'http://127.0.0.1:{port}'+path, data=data, headers=headers, method=method)
            try:
                with urllib.request.urlopen(req, timeout=6) as response:
                    return response.status, response.read(4096)
            except urllib.error.HTTPError as error:
                return error.code, error.read(4096)

        try:
            print('Building and starting isolated APISIX/mock services', flush=True)
            run(['config', '--quiet'])
            run(['up', '-d', '--build', '--wait', '--wait-timeout', '120'])
            run(['exec', '-T', 'apisix', 'apisix', 'test'])
            # Allow the standalone config watcher to load routes.
            deadline = time.monotonic()+15
            while request(credential=None)[0] != 401:
                if time.monotonic()>deadline:
                    raise RuntimeError('Internal AI route did not load')
                time.sleep(.2)
            before = calls()
            assert request(credential=None)[0] == 401
            assert request(credential='wrong')[0] == 401
            assert request(credential=None, cookie='session=untrusted')[0] == 401
            assert calls() == before, 'Unauthenticated request invoked provider'
            print('Missing/wrong credentials and cookie-only rejected before provider', flush=True)
            for payload in [dict(body, model='arbitrary'), dict(body, stream=True), dict(body, endpoint='http://example.invalid'), dict(body, messages=[]), b'{', dict(body, messages=[dict(role='user', content='x'*9000)])]:
                assert request(payload)[0] in (400, 413)
            assert request(path='/internal/ai/chat/completions?endpoint=http://example.invalid')[0] == 400
            assert request(method='GET')[0] == 405
            for path in ['/internal', '/internal/ai', '/internal/ai/chat/completions/extra']:
                assert request(path=path)[0] == 404
            assert calls() == before, 'Invalid request invoked provider'
            code, response = request(cookie='session=must-be-stripped')
            assert code == 200, f"Authenticated inference returned HTTP {code}; body bytes {len(response)}; calls {calls()}"
            result = json.loads(response)
            assert code == 200 and result['model'] == body['model']
            assert result['choices'][0]['message']['content'].startswith('SIMULATED:')
            assert 'usage' not in result and len(response) <= 2048
            assert calls() == before+1
            print('Authenticated request traversed ai-proxy; model/size/namespace restrictions passed', flush=True)
            candidates = [dict(product_id='12345678-1234-1234-1234-123456789012', name='Demo oats', package_label='1 bag')]
            grounded = dict(body, messages=[dict(role='user', content=json.dumps(dict(version=1, scenario='breakfast for two', candidates=candidates)))])
            code, response = request(grounded)
            assert code == 200 and len(response) <= 2048
            structured = json.loads(json.loads(response)['choices'][0]['message']['content'])
            assert structured['version'] == 1 and structured['text'].startswith('SIMULATED:')
            assert structured['suggestions'] == [dict(product_id=candidates[0]['product_id'], quantity=1, reason='Demo scenario catalog candidate.')]
            assert calls() == before+2
            print('Structured candidate-grounded scenario passed through APISIX', flush=True)

            for scenario, expected in [('malformed', 200), ('rate-limit', 429), ('server-error', 500), ('delay', 504)]:
                services['mock-ai']['environment']['MOCK_SCENARIO'] = scenario
                config.write_text(json.dumps(compose))
                run(['up', '-d', '--no-deps', '--wait', 'mock-ai'])
                assert calls() == 0
                start = time.monotonic()
                code, response = request()
                elapsed = time.monotonic()-start
                assert code == expected, f'{scenario}: unexpected HTTP {code}'
                if scenario == 'malformed':
                    try:
                        json.loads(response)
                    except json.JSONDecodeError:
                        pass
                    else:
                        raise AssertionError('Malformed output was not preserved')
                if scenario == 'delay':
                    assert .8 <= elapsed < 2.5, 'Timeout not bounded'
                time.sleep(.2)
                assert calls() == 1, f'{scenario}: retry detected'
                print(f'{scenario}: expected status, one provider invocation; elapsed {elapsed:.2f}s', flush=True)
            print('AI gateway verification passed (mock protocol only)', flush=True)
        finally:
            subprocess.run(command+['down', '-v', '--remove-orphans', '--rmi', 'local'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
