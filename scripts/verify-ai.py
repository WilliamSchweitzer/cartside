#!/usr/bin/env python3
"""Disposable AI gateway verification. Never reads .env or uses developer volumes."""
from concurrent.futures import ThreadPoolExecutor
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
        # Keep disposable logs available for correlation and redaction assertions.
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            api_port = sock.getsockname()[1]
        routes = yaml.safe_load((ROOT/'infra/apisix/apisix.yaml').read_text())
        health = next(r for r in routes['routes'] if r['id'] == 'health')
        health['uris'].append('/api/assistant/messages')
        health['upstream']['nodes'] = {f'host.docker.internal:{api_port}': 1}
        route_file = folder/'routes.yaml'
        route_file.write_text(yaml.safe_dump(routes) + '\n#END\n')
        route_file.chmod(0o600)
        services['apisix']['volumes'][1] = f'{route_file}:/usr/local/apisix/conf/apisix.yaml:ro'
        config = folder/'compose.json'
        config.write_text(json.dumps(compose)); config.chmod(0o600)
        command = ['docker', 'compose', '--env-file', '/dev/null', '-p', project, '-f', str(config)]

        def run(args, capture=False):
            result = subprocess.run(command+args, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            if result.returncode:
                raise RuntimeError('Compose step failed: ' + ' '.join(args[:2]))
            return result.stdout if capture else None

        def calls():
            return json.loads(run(['exec', '-T', 'mock-ai', '/mock-ai', '-probe', 'http://127.0.0.1:8090/test/stats'], True))['calls']

        def correlation(scenario):
            result_file = folder/'correlation.json'
            test_env = dict(env, CORRELATION_GATEWAY=f'http://127.0.0.1:{port}',
                            CORRELATION_SERVICE_KEY=key, CORRELATION_API_PORT=str(api_port),
                            CORRELATION_RESULT=str(result_file), CORRELATION_SCENARIO=scenario)
            result = subprocess.run(['go', 'test', './internal/observability', '-run', '^TestGatewayCorrelation$', '-count=1'],
                                    cwd=ROOT/'backend', env=test_env, capture_output=True, text=True)
            if result.returncode:
                raise RuntimeError('Go gateway correlation fixture failed: ' + result.stdout)
            evidence = json.loads(result_file.read_text())
            time.sleep(3.2)  # Nginx access log flush interval.
            logs = run(['logs', '--no-log-prefix', 'apisix', 'mock-ai'], True)
            events = []
            for line in logs.splitlines():
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
            ordinary, assistant = evidence['ids']
            assert any(e.get('request_id') == ordinary and e.get('event') == 'gateway_request_completed' for e in events)
            assert sum(e.get('request_id') == assistant and e.get('event') == 'gateway_request_completed' for e in events) == 2
            assert any(e.get('request_id') == assistant and e.get('component') == 'mock' for e in events)
            for secret in [key, 'session=must-be-stripped', 'browser-supplied-must-replace', 'breakfast for two', 'private-callback-code', 'private-callback-state']:
                assert secret not in logs, 'Sensitive data appeared in gateway/mock logs'
            print(f'{scenario}: matching gateway/Go/outbound/mock IDs; safe outcome verified', flush=True)

        body = dict(model='cartside-mock-v1', messages=[dict(role='user', content='breakfast for two')], stream=False)

        def internal_ids():
            supplied = ['internal-safe-id', 'bad/id', 'x'*512] + [f'concurrent-{i}' for i in range(12)]
            def invoke(request_id):
                req = urllib.request.Request(f'http://127.0.0.1:{port}/internal/ai/chat/completions',
                    data=json.dumps(body).encode(), headers={'Content-Type': 'application/json',
                    'X-Cartside-AI-Key': key, 'X-Request-ID': request_id})
                with urllib.request.urlopen(req, timeout=6) as response:
                    response.read()
                    actual = response.headers['X-Request-ID']
                if request_id == 'bad/id' or len(request_id) > 64:
                    assert actual != request_id and len(actual) == 36
                else:
                    assert actual == request_id
                return actual
            with ThreadPoolExecutor(max_workers=6) as executor:
                ids = list(executor.map(invoke, supplied))
            time.sleep(3.2)
            logs = run(['logs', '--no-log-prefix', 'apisix', 'mock-ai'], True)
            events = []
            for line in logs.splitlines():
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
            for request_id in ids:
                assert sum(e.get('request_id') == request_id and e.get('component') == 'mock' for e in events) == 1
            print('Authenticated ID validation and concurrent provider isolation passed', flush=True)

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

            # A callback query must never be present in either access or error logs.
            request(path='/auth/callback?code=private-callback-code&state=private-callback-state', method='GET', credential=None)
            correlation('normal')
            internal_ids()

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
                correlation(scenario)
                print(f'{scenario}: expected status, one provider invocation; elapsed {elapsed:.2f}s', flush=True)
            print('AI gateway verification passed (mock protocol only)', flush=True)
        finally:
            subprocess.run(command+['down', '-v', '--remove-orphans', '--rmi', 'local'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
