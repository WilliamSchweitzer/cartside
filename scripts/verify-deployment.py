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
import threading
import time
import re
import shutil
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
        env.update(IMAGE_TAG=os.environ.get('CARTSIDE_DEPLOY_IMAGE_TAG','demo-001'), POSTGRES_USER='e2e', POSTGRES_DB='e2e', KEYCLOAK_ADMIN_USERNAME='test-admin')
        for k in ('POSTGRES_PASSWORD','KEYCLOAK_DB_PASSWORD','KEYCLOAK_ADMIN_PASSWORD','CARTSIDE_OIDC_CLIENT_SECRET','CARTSIDE_SESSION_SECRET','CARTSIDE_AI_SERVICE_KEY','APP_RUNTIME_PASSWORD','APP_MIGRATION_PASSWORD'):
            env[k] = secrets.token_hex(32)
        def run(args, **kw):
            p = subprocess.run(args, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300, **kw)
            if p.returncode:
                # Emit only safe operation identifiers; never subprocess output.
                raise RuntimeError('Failed deployment operation: '+ ' '.join(args[args.index(str(config))+1:]) if 'compose' in args else 'Deployment subprocess failed (sensitive output suppressed)')
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
        s['api']['logging']={'driver':'json-file','options':{'max-size':'10m','max-file':'3'}}
        (folder/'grafana-password').write_text(secrets.token_hex(32))
        (folder/'grafana-password').chmod(0o644) # Disposable; parent directory is private.
        s['grafana']['volumes'][-1]=f'{folder}/grafana-password:/run/secrets/grafana_admin_password:ro'
        s['edge']['ports']=['127.0.0.1:18443:8443']
        s['edge']['volumes']=[f'{folder}/edge.conf:/etc/nginx/conf.d/default.conf:ro',f'{certs}:/certs:ro']
        s['keycloak']['volumes'][0]=f'{folder}/realm.json:/opt/keycloak/data/import/cartside-realm.json:ro'
        s['keycloak']['environment']['KC_HOSTNAME']=auth
        s['apisix']['volumes'][1]=f'{folder}/apisix.yaml:/usr/local/apisix/conf/apisix.yaml:ro'
        s['api']['environment'].update(BROWSER_ORIGIN=origin,OIDC_ISSUER=auth+'/realms/cartside')
        config=folder/'compose.json';config.write_text(json.dumps(compose));config.chmod(0o600)
        command=['docker','compose','--env-file','/dev/null','-p',project,'-f',str(config)]
        samples = []
        phase = ['startup']
        stop = threading.Event()
        def sample():
            while not stop.is_set():
                ids = subprocess.run(command+['ps','-q'],env=env,capture_output=True,text=True).stdout.split()
                if ids:
                    result = subprocess.run(['docker','stats','--no-stream','--format','{{json .}}',*ids],capture_output=True,text=True)
                    for line in result.stdout.splitlines():
                        samples.append(dict(phase=phase[0], **json.loads(line)))
                stop.wait(2)
        def capture(args, input=None):
            result = subprocess.run(command+args,env=env,input=input,capture_output=True,text=True,timeout=120)
            if result.returncode: raise RuntimeError('Private deployment check failed: '+args[0])
            return result.stdout.strip()
        def sql(statement):
            return capture(['exec','-T','postgres','psql','-X','-U','e2e','-d','e2e','-v','ON_ERROR_STOP=1','-qAt'],statement)
        def disk(label):
            result=subprocess.run(['docker','system','df','--format','{{json .}}'],capture_output=True,text=True,check=True)
            print('DISK '+label+' '+result.stdout.strip(),flush=True)
            print('HOST-DISK '+label+' '+str(shutil.disk_usage(ROOT)),flush=True)
            ids=capture(['--profile','observability','--profile','tools','ps','-aq']).split()
            if ids:
                data=json.loads(subprocess.check_output(['docker','inspect','--size',*ids],text=True))
                print('PROJECT-WRITABLE-BYTES '+label+' '+str(sum(c.get('SizeRw',0) for c in data)),flush=True)
            volumes=subprocess.check_output(['docker','volume','ls','-q','--filter','label=com.docker.compose.project='+project],text=True).split()
            for volume in volumes:
                size=subprocess.check_output(['docker','run','--rm','--network','none','--mount',f'type=volume,src={volume},dst=/measure,readonly','postgres:17.10-bookworm','du','-sk','/measure'],text=True).split()[0]
                print('PROJECT-VOLUME-KIB '+label+' '+volume+' '+size,flush=True)
        disk('before')
        monitor=threading.Thread(target=sample,daemon=True);monitor.start()
        try:
            print('Starting isolated ARM64 deployment',flush=True)
            run(command+['up','-d','--wait','postgres'])
            run(command+['run','--rm','--no-deps','app-db'])
            run(command+['run','--rm','--no-deps','keycloak-db'])
            run(command+['run','--rm','--no-deps','migrate'])
            run(command+['run','--rm','--no-deps','--entrypoint','seed','migrate'])
            # Simulate the previous deployment's bootstrap-owned application objects.
            sql("ALTER TABLE products OWNER TO e2e; ALTER TABLE carts OWNER TO e2e; ALTER TABLE cart_items OWNER TO e2e; ALTER TABLE orders OWNER TO e2e; ALTER TABLE order_items OWNER TO e2e; ALTER TABLE goose_db_version OWNER TO e2e; ALTER FUNCTION reject_order_change() OWNER TO e2e;")
            # Repeat bootstrap against that existing database; preserve seeded data.
            run(command+['run','--rm','--no-deps','app-db'])
            run(command+['run','--rm','--no-deps','migrate'])
            assert sql("SELECT count(*) FROM products") == '100'
            assert sql("SELECT has_database_privilege('cartside_runtime','keycloak','CONNECT') OR pg_has_role('cartside_runtime','keycloak','MEMBER')") == 'f'
            assert sql("SELECT bool_and(tableowner='cartside_migrate') FROM pg_tables WHERE schemaname='public' AND tablename IN ('products','carts','cart_items','orders','order_items','goose_db_version')") == 't'
            assert sql("SELECT has_table_privilege('cartside_runtime','products','UPDATE') OR has_column_privilege('cartside_runtime','products','price_cents','UPDATE') OR has_table_privilege('cartside_runtime','orders','DELETE') OR has_table_privilege('cartside_runtime','goose_db_version','SELECT') OR has_schema_privilege('cartside_runtime','public','CREATE') OR has_database_privilege('cartside_runtime',current_database(),'TEMP') OR pg_has_role('cartside_runtime','cartside_migrate','MEMBER')") == 'f'
            # A runtime migration must fail, without altering schema/data.
            denied=subprocess.run(command+['run','--rm','--no-deps','--entrypoint','migrate','api','up'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=60)
            assert denied.returncode != 0
            run(command+['up','-d','--wait','--wait-timeout','240'])
            assert sql("SELECT count(*) FROM pg_stat_activity WHERE usename='cartside_runtime'") != '0'
            discovery=json.loads(capture(['exec','-T','api','curl','-fsS','http://keycloak:8080/realms/cartside/.well-known/openid-configuration']))
            assert discovery['issuer']==auth+'/realms/cartside'
            assert discovery['authorization_endpoint'].startswith(auth+'/')
            assert discovery['token_endpoint'].startswith('http://keycloak:8080/')
            print('Canonical issuer and internal token backchannel discovery passed',flush=True)
            ids=capture(['ps','-q']).split()
            inspected=json.loads(subprocess.check_output(['docker','inspect',*ids],text=True))
            for container in inspected:
                ports=container['HostConfig']['PortBindings'] or {}
                assert not ports or (container['Config']['Labels']['com.docker.compose.service']=='edge' and list(ports)==['8443/tcp'])
                assert container['State'].get('Health',{}).get('Status') == 'healthy'
            print('Runtime role, migration ownership, repeated provisioning, health and private ports passed',flush=True)
            disk('idle')
            phase[0]='idle';time.sleep(10)
            phase[0]='browser-load'
            private=folder/'browser.json';private.write_text(json.dumps(dict(origin=origin,users=users,compose=command,deployment=True)));private.chmod(0o600)
            env['CARTSIDE_E2E_CONFIG']=str(private)
            env['CARTSIDE_E2E_HTTPS']='1'
            # Chromium resolves .localhost itself; Node's HTTP client uses OS DNS.
            # Scope the same loopback mapping to disposable Node processes only.
            resolver=folder/'localhost.cjs'
            resolver.write_text("""const dns = require('node:dns');
const lookup = dns.lookup;
const promiseLookup = dns.promises.lookup;
dns.promises.lookup = async function(host, options = {}) {
  if (!['cartside.localhost','auth.cartside.localhost'].includes(host)) return promiseLookup.apply(this, arguments);
  if (options.all) return options.family === 6 ? [] : [{address:'127.0.0.1',family:4}];
  return {address:'127.0.0.1',family:4};
};
dns.lookup = function(host, options, callback) {
  if (!['cartside.localhost','auth.cartside.localhost'].includes(host)) return lookup.apply(this, arguments);
  if (typeof options === 'function') { callback = options; options = {}; }
  process.nextTick(() => options && options.all ? callback(null, [{address:'127.0.0.1',family:4}]) : callback(null,'127.0.0.1',4));
};
""")
            env['NODE_OPTIONS']='--require='+str(resolver)

            p=subprocess.run(['npm','run','test:e2e'],cwd=ROOT/'frontend',env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=600)
            for line in p.stdout.splitlines():
                if line.startswith('CARTSIDE-E2E '): print(line,flush=True)
            if p.returncode: raise RuntimeError('HTTPS browser suite failed; sensitive diagnostics suppressed')
            print('HTTPS deployment suite passed',flush=True)
            before=sql("SELECT json_build_array((SELECT count(*) FROM products),(SELECT count(*) FROM carts),(SELECT count(*) FROM orders),(SELECT count(*) FROM order_items))")
            run(command+['restart','postgres','keycloak','api','apisix','frontend','mock-ai','edge'])
            run(command+['up','-d','--wait','--wait-timeout','240'])
            assert before==sql("SELECT json_build_array((SELECT count(*) FROM products),(SELECT count(*) FROM carts),(SELECT count(*) FROM orders),(SELECT count(*) FROM order_items))")
            # Browser rerun verifies persisted identity users and application data after restart.
            p=subprocess.run(['npm','run','test:e2e'],cwd=ROOT/'frontend',env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=600)
            for line in p.stdout.splitlines():
                if line.startswith('CARTSIDE-E2E '): print(line,flush=True)
            assert p.returncode == 0, 'Post-restart browser suite failed'
            print('Restart persistence and post-restart HTTPS suite passed',flush=True)
            phase[0]='monitoring'
            run(command+['--profile','observability','up','-d','--wait','prometheus','grafana'])
            time.sleep(20)
            targets=json.loads(capture(['exec','-T','api','curl','-fsS','http://prometheus:9090/api/v1/targets']))
            assert len(targets['data']['activeTargets'])==2
            assert all(t['health']=='up' for t in targets['data']['activeTargets'])
            assert json.loads(capture(['exec','-T','api','curl','-fsS','http://grafana:3000/api/health']))['database']=='ok'
            for name in ('prometheus','grafana'):
                cid=capture(['ps','-q',name])
                item=json.loads(subprocess.check_output(['docker','inspect',cid],text=True))[0]
                assert not item['HostConfig']['PortBindings']
            print('Private monitoring health and both scrape targets passed',flush=True)
            started=time.monotonic()
            run(command+['--profile','observability','stop'])
            stopped=json.loads(subprocess.check_output(['docker','inspect',*capture(['--profile','observability','--profile','tools','ps','-aq']).split()],text=True))
            exits={c['Config']['Labels']['com.docker.compose.service']: c['State']['ExitCode'] for c in stopped}
            print('Shutdown exit codes: '+json.dumps(exits),flush=True)
            # The Keycloak JVM reports handled SIGTERM as 128+15.
            # Reject forced SIGKILL (137), OOM, and unexpected application exits.
            assert all(c['State']['ExitCode'] in ((0,143) if c['Config']['Labels']['com.docker.compose.service'] == 'keycloak' else (0,)) and not c['State']['OOMKilled'] for c in stopped)
            api_id=capture(['ps','-aq','api'])
            api_logs=subprocess.run(['docker','logs',api_id],capture_output=True,text=True,check=True)
            events=[json.loads(line).get('msg') for line in (api_logs.stdout+api_logs.stderr).splitlines() if line.startswith('{')]
            assert events.count('server_stopping')>=2 and events.count('server_stopped')>=2
            print('Graceful shutdown: Go drain logged, expected exits, no SIGKILL/OOM; seconds='+str(round(time.monotonic()-started,2)),flush=True)
            disk('after-test')
        finally:
            stop.set();monitor.join(timeout=15)
            # Aggregate observed samples, not production capacity estimates.
            def mib(value):
                number,unit=re.match(r'([0-9.]+)([A-Za-z]+)',value.split(' / ')[0]).groups()
                return float(number)*{'B':1/1048576,'KiB':1/1024,'MiB':1,'GiB':1024}[unit]
            for label in ('startup','idle','browser-load','monitoring'):
                rows=[r for r in samples if r['phase']==label]
                # Per-service sampled maxima; their sum is a conservative envelope,
                # not a simultaneous peak. CPU 100% means one logical core.
                peaks={}
                for row in rows:
                    peak=peaks.setdefault(row['Name'],dict(memory_mib=0,cpu_percent=0))
                    peak['memory_mib']=round(max(peak['memory_mib'],mib(row['MemUsage'])),2)
                    peak['cpu_percent']=max(peak['cpu_percent'],float(row['CPUPerc'].strip('%')))
                print('RESOURCE '+label+' '+json.dumps(peaks),flush=True)
            subprocess.run(command+['--profile','observability','--profile','tools','down','-v','--remove-orphans'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=60,check=True)
            assert not subprocess.check_output(['docker','ps','-aq','--filter','label=com.docker.compose.project='+project],text=True).strip()
            assert not subprocess.check_output(['docker','volume','ls','-q','--filter','label=com.docker.compose.project='+project],text=True).strip()
            print('Disposable project cleanup verified',flush=True)

if __name__=='__main__':main()
