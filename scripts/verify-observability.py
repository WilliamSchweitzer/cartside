#!/usr/bin/env python3
"""Isolated local monitoring check. Never reads .env or touches developer resources."""
import json
import os
from pathlib import Path
import secrets
import socket
import sys
import subprocess
import tempfile
import time
import urllib.request
import urllib.error
import yaml
ROOT=Path(__file__).resolve().parents[1]
def port():
 with socket.socket() as s:
  s.bind(('127.0.0.1',0));return s.getsockname()[1]
def get(url,data=None,headers=None):
 try:
  with urllib.request.urlopen(urllib.request.Request(url,data=data,headers=headers or {}),timeout=10) as r:return r.read().decode()
 except urllib.error.HTTPError as e:return e.read().decode()
def main():
 project='cartside-monitoring-'+secrets.token_hex(5)
 env={k:v for k,v in os.environ.items() if not k.startswith('COMPOSE_')}
 env.update({k:secrets.token_hex(32) for k in ['CARTSIDE_AI_SERVICE_KEY','CARTSIDE_OIDC_CLIENT_SECRET','CARTSIDE_SESSION_SECRET']})
 api,metrics,gateway,prom,grafana=[port() for _ in range(5)]
 with tempfile.TemporaryDirectory(prefix=project) as tmp:
  folder=Path(tmp);password=secrets.token_hex(32);(folder/'password').write_text(password);(folder/'password').chmod(0o644)
  config=yaml.safe_load((ROOT/'compose.yaml').read_text());config['services']={k:v for k,v in config['services'].items() if k in ['apisix','mock-ai','prometheus','grafana']}
  services=config['services']
  for service in services.values():
   service.pop('profiles',None)
   service['volumes']=[str(ROOT/v[2:]) if v.startswith('./') else v for v in service.get('volumes',[])]
  services['apisix']['ports']=[f'127.0.0.1:{gateway}:9080']
  services['mock-ai']['build']['context']=str(ROOT)
  services['mock-ai']['environment']={'MOCK_TEST_MODE':'1','MOCK_SCENARIO':'normal'}
  routes=yaml.safe_load((ROOT/'infra/apisix/apisix.yaml').read_text())
  health=next(r for r in routes['routes'] if r['id']=='health');health['uris'].append('/api/assistant/messages');health['upstream']['nodes']={f'host.docker.internal:{api}':1}
  (folder/'routes.yaml').write_text(yaml.safe_dump(routes)+'\n#END\n')
  services['apisix']['volumes'][1]=f'{folder}/routes.yaml:/usr/local/apisix/conf/apisix.yaml:ro'
  scrape=yaml.safe_load((ROOT/'infra/observability/prometheus.yaml').read_text());scrape['scrape_configs'][0]['static_configs'][0]['targets']=[f'host.docker.internal:{metrics}']
  (folder/'prometheus.yaml').write_text(yaml.safe_dump(scrape))
  services['prometheus']['volumes'][0]=f'{folder}/prometheus.yaml:/etc/prometheus/prometheus.yaml:ro';services['prometheus']['ports']=[f'127.0.0.1:{prom}:9090']
  services['grafana']['volumes'][-1]=f'{folder}/password:/run/secrets/grafana_admin_password:ro';services['grafana']['ports']=[f'127.0.0.1:{grafana}:3000'];services['grafana']['environment']['GF_SECURITY_ADMIN_USER']='local-admin'
  (folder/'compose.json').write_text(json.dumps(config))
  cmd=['docker','compose','--env-file','/dev/null','-p',project,'-f',str(folder/'compose.json')]
  def run(args):
   p=subprocess.run(cmd+args,env=env,capture_output=True,text=True)
   if p.returncode:raise RuntimeError('Compose failed: '+' '.join(args)+'\n'+p.stderr)
   return p.stdout
  fixture=None
  try:
   print('Starting isolated monitoring stack',flush=True)
   run(['config','--quiet']);run(['up','-d','--build','apisix','mock-ai','prometheus','grafana'])
   run(['exec','-T','prometheus','promtool','check','config','/etc/prometheus/prometheus.yaml'])
   run(['exec','-T','apisix','apisix','test'])
   fixture_env=dict(env,MONITORING_FIXTURE_DIR=tmp,MONITORING_GATEWAY=f'http://127.0.0.1:{gateway}',MONITORING_API_ADDR=f'0.0.0.0:{api}',MONITORING_METRICS_ADDR=f'0.0.0.0:{metrics}')
   with (folder/'fixture.log').open('w') as log:
    fixture=subprocess.Popen(['go','test','./internal/observability','-run','^TestMonitoringStack$','-count=1'],cwd=ROOT/'backend',env=fixture_env,stdout=log,stderr=log)
   def wait(fn):
    for _ in range(100):
     try:
      if fn():return
     except (OSError,ValueError,KeyError):pass
     time.sleep(1)
    raise RuntimeError('Timed out waiting for monitoring readiness')
   wait(lambda:(folder/'ready').exists())
   base=f'http://127.0.0.1:{prom}'
   def query(expr):
    from urllib.parse import urlencode
    result=json.loads(get(base+'/api/v1/query?'+urlencode({'query':expr})))
    assert result['status']=='success',result
    return result['data']['result']
   wait(lambda:len(query('up == 1'))==2)
   for _ in range(3):get(f'http://127.0.0.1:{gateway}/internal/private-metric-fixture?prompt=secret-fixture')
   get(f'http://127.0.0.1:{api}/unknown-sensitive-fixture?product=secret-fixture',headers={'X-Request-ID':'private-metric-id','X-User-ID':'private-metric-identity'})
   for _ in range(3):get(f'http://127.0.0.1:{gateway}/api/health/live')
   get(f'http://127.0.0.1:{gateway}/api/assistant/messages',b'{}')
   services['mock-ai']['environment']['MOCK_SCENARIO']='server-error'
   (folder/'compose.json').write_text(json.dumps(config))
   run(['up','-d','--wait','mock-ai'])
   get(f'http://127.0.0.1:{gateway}/api/assistant/messages',b'{}')
   wait(lambda:len(query('cartside_assistant_calls_total'))>=2)
   time.sleep(32)
   dashboard=json.loads((ROOT/'infra/observability/grafana/dashboards/cartside.json').read_text())
   for panel in dashboard['panels']:
    wait(lambda:bool(query(panel['targets'][0]['expr'])))
   exposition=get(f'http://127.0.0.1:{metrics}/metrics')
   gateway_exposition=run(['exec','-T','apisix','resty','-I','/usr/local/apisix/deps/share/lua/5.1','-e', 'local h=require("resty.http").new(); local r=assert(h:request_uri("http://127.0.0.1:9091/apisix/prometheus/metrics")); print(r.body)'])
   assert 'apisix_http_status' in gateway_exposition
   assert 'matched_uri="/api/health/live"' in gateway_exposition
   assert 'route="internal-ai"' in gateway_exposition
   assert query('cartside_assistant_calls_total{outcome="success"}')[0]['value'][1]=='1'
   assert query('cartside_assistant_calls_total{outcome="provider_unavailable"}')[0]['value'][1]=='1'
   assert query('cartside_assistant_call_duration_seconds_count{outcome="provider_unavailable"}')[0]['value'][1]=='1'
   assert not query('{__name__=~"apisix_llm_.*"}')
   assert float(query('cartside_assistant_call_duration_seconds_sum{outcome="success"}')[0]['value'][1])>0
   assert float(query('cartside_http_request_duration_seconds_sum{route="/assistant/messages"}')[0]['value'][1])>0
   exposition += gateway_exposition
   for value in [env['CARTSIDE_AI_SERVICE_KEY'],'breakfast for two','private-metric-fixture','secret-fixture','unknown-sensitive-fixture','private-metric-id','private-metric-identity']:
    assert value not in exposition
   wait(lambda:json.loads(get(f'http://127.0.0.1:{grafana}/api/health')).get('database')=='ok')
   import base64
   auth={'Authorization':'Basic '+base64.b64encode(('local-admin:'+password).encode()).decode()}
   provisioned=json.loads(get(f'http://127.0.0.1:{grafana}/api/dashboards/uid/cartside-overview',headers=auth))
   assert provisioned['dashboard']['uid']=='cartside-overview'
   print('PASS: both targets UP; ordinary/success/failure traffic; all dashboard queries return series; Grafana provisioning/auth; sensitive exposition fixtures absent.',flush=True)
   if '--browser' in sys.argv:
    subprocess.run(['node',str(ROOT/'scripts/verify-grafana.mjs')],env=dict(env,GRAFANA_TEST_URL=f'http://127.0.0.1:{grafana}',GRAFANA_TEST_PASSWORD=password),check=True)
   else:
    print('Browser rendering not checked; pass --browser to check with installed Playwright/Chromium.')
  finally:
   (folder/'stop').touch()
   if fixture:
    try:fixture.wait(timeout=15)
    except subprocess.TimeoutExpired:fixture.terminate();fixture.wait()
   run(['down','-v','--remove-orphans'])
   if fixture and fixture.returncode != 0:raise RuntimeError('Go monitoring fixture failed')
if __name__=='__main__':main()
