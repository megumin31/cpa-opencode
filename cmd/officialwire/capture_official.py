# Actual official-client integration test. All credentials and endpoints are synthetic.
import argparse, hashlib, json, os, pathlib, signal, subprocess, tempfile, threading

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from protocol_fixtures import protocol_spec, response_frames, tool_result
parser=argparse.ArgumentParser(description='Capture official OpenCode 2.0.29 against a fake loopback Console and inference service')
parser.add_argument('--binary',required=True,help='Verified official 2.0.29 Linux x64 binary')
parser.add_argument('--output',required=True,help='Directory for newly generated fake-only capture artifacts')
parser.add_argument('--scenario',choices=['text','continuation','tool','tool-error'],default='text',help='Synthetic text, resumed session, successful read tool, or invalid tool arguments')
parser.add_argument('--protocol', choices=['chat','responses','anthropic','gemini'], default='chat')
args=parser.parse_args()
if args.scenario == 'continuation' and args.protocol != 'chat':
 parser.error('continuation is currently validated only for --protocol chat')
PACKAGE, BASE_PATH, POST_PATH = protocol_spec(args.protocol)
BIN=str(pathlib.Path(args.binary).resolve())
expected='7e23471f4ebe2313d78baacc9ac96fe9e197ce26d808de4b0fcc37efbe473dad'
if hashlib.sha256(pathlib.Path(BIN).read_bytes()).hexdigest()!=expected:
 raise SystemExit('Refusing an unverified binary: SHA256 does not match the audited official 2.0.29 Linux x64 build')
output=pathlib.Path(args.output).resolve();output.mkdir(parents=True,exist_ok=True)
ROOT=pathlib.Path(tempfile.mkdtemp(prefix='fake-official-wire-',dir=output))
BASE='http://127.0.0.1:18080'
KEY='fake-official-wire-key'
CAP=[]
PROXY=[]
class DenyProxy(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_CONNECT(self):
  PROXY.append({'method':'CONNECT','target':self.path});self.send_response(502);self.end_headers()
 def do_GET(self):
  PROXY.append({'method':'GET','target':self.path});self.send_response(502);self.end_headers()
 def do_POST(self):
  PROXY.append({'method':'POST','target':self.path});self.send_response(502);self.end_headers()
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_GET(self):
  CAP.append({'method':'GET','path':self.path,'headers':dict(self.headers)})
  if self.path=='/console/api/v2/config':
   data={'providers':{'opencode':{'package':'aisdk:'+PACKAGE,'settings':{'baseURL':BASE+BASE_PATH,'headers':{'x-opencode-org-id':'fake-config-org'}},'models':{'space-bunny-free':{'name':'Local Fake Space Bunny'}}}}}
  elif self.path=='/catalog.json':data=CATALOG
  else:self.send_response(404);self.end_headers();return
  raw=json.dumps(data).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def do_POST(self):
  raw=self.rfile.read(int(self.headers.get('Content-Length','0')))
  CAP.append({'method':'POST','path':self.path,'headers':dict(self.headers),'body':json.loads(raw) if raw else None})
  if self.path!=POST_PATH:self.send_response(404);self.end_headers();return
  tool_args = None
  if args.scenario in ('tool','tool-error') and len([x for x in CAP if x['method'] == 'POST']) == 1:
   tool_args = {('path' if args.scenario=='tool' else 'invalid_field'):str(ROOT/'work'/'fixture.txt')}
  out=response_frames(args.protocol, tool_args)
  self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Content-Length',str(len(out)));self.end_headers();self.wfile.write(out)

CATALOG={'opencode':{'id':'opencode','env':['OPENCODE_API_KEY'],'npm':PACKAGE,'api':BASE+BASE_PATH,'name':'Local Fake Console','models':{'space-bunny-free':{'id':'space-bunny-free','name':'Local Fake Space Bunny','tool_call':args.scenario in ('tool','tool-error'),'reasoning':False,'modalities':{'input':['text'],'output':['text']},'limit':{'context':100000,'output':16},'cost':{'input':0,'output':0}}}}}
(ROOT/'catalog.json').write_text(json.dumps(CATALOG))
for d in ['home','data','config','cache','state','work']: (ROOT/d).mkdir(exist_ok=True)
ENV={'PATH':'/usr/bin:/bin','HOME':str(ROOT/'home'),'XDG_DATA_HOME':str(ROOT/'data'),'XDG_CONFIG_HOME':str(ROOT/'config'),'XDG_CACHE_HOME':str(ROOT/'cache'),'XDG_STATE_HOME':str(ROOT/'state'),'OPENCODE_TEST_HOME':str(ROOT/'home'),'OPENCODE_DISABLE_AUTOUPDATE':'true','OPENCODE_DISABLE_MODELS_FETCH':'true','OPENCODE_MODELS_PATH':str(ROOT/'catalog.json'),'OPENCODE_DISABLE_FILEWATCHER':'true','OPENCODE_DISABLE_FFF':'true','OPENCODE_DISABLE_PROJECT_CONFIG':'true','OPENCODE_DISABLE_CHANNEL_DB':'true','NO_PROXY':'127.0.0.1,localhost,0.0.0.0,::1','no_proxy':'127.0.0.1,localhost,0.0.0.0,::1','HTTP_PROXY':'http://127.0.0.1:18082','HTTPS_PROXY':'http://127.0.0.1:18082','http_proxy':'http://127.0.0.1:18082','https_proxy':'http://127.0.0.1:18082','ALL_PROXY':'http://127.0.0.1:18082','OPENCODE_MODELS_URL':BASE+'/catalog.json','TERM':'dumb'}
proxy=ThreadingHTTPServer(('127.0.0.1',18082),DenyProxy);threading.Thread(target=proxy.serve_forever,daemon=True).start()
server=ThreadingHTTPServer(('127.0.0.1',18080),Handler);threading.Thread(target=server.serve_forever,daemon=True).start()
def run(label, args, timeout=35):
 print('RUN', label, flush=True)
 stdout, stderr = b'', b''
 process = subprocess.Popen([BIN] + args, env=ENV, cwd=ROOT/'work',
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            start_new_session=True)
 try:
  stdout, stderr = process.communicate(timeout=timeout)
 except subprocess.TimeoutExpired:
  os.killpg(process.pid, signal.SIGKILL)
  stdout, stderr = process.communicate()
  raise RuntimeError('Official local command timed out: ' + label)
 finally:
  (ROOT/(label+'.stdout')).write_bytes(stdout)
  (ROOT/(label+'.stderr')).write_bytes(stderr)
  (ROOT/'official-capture.json').write_text(json.dumps(CAP, indent=2))
  (ROOT/'proxy-rejections.json').write_text(json.dumps(PROXY, indent=2))
 print('EXIT', process.returncode, flush=True)
 if process.returncode != 0:
  raise RuntimeError('Official local command failed: ' + label)
if __name__=='__main__':
 CONFIG={'model':'opencode/space-bunny-free','default_agent':'probe','agents':{'probe':{'mode':'primary','system':'Answer briefly.','steps':2,'permissions':[{'action':'*','resource':'*','effect':'deny'}]},'title':{'disabled':True}},'update':'disable','share':'disabled','snapshots':False,'formatter':False,'lsp':False,'websearch':False,'warming':False,'compaction':{'auto':False}}
 if args.scenario in ('tool','tool-error'):
  CONFIG['agents']['probe']['permissions']=[{'action':'*','resource':'*','effect':'deny'},{'action':'read','resource':'*','effect':'allow'}]
  (ROOT/'work'/'fixture.txt').write_text('Synthetic tool fixture only.\n')
 ENV['OPENCODE_CONFIG_CONTENT']=json.dumps(CONFIG)
 AUTH=[{'id':'cred_fake_local','integrationID':'opencode','label':'fake-local','active':True,'value':{'type':'key','key':KEY,'metadata':{'server':BASE+'/console','orgID':'fake-org'}}}]
 (ROOT/'fake-auth.json').write_text(json.dumps(AUTH))
 try:
  run('preflight-paths', ['debug', 'paths'])
  run('import-fake', ['auth', 'import', str(ROOT/'fake-auth.json'), '--standalone'])
  run('run-minimal-fake', ['run', '--standalone', '--model', 'opencode/space-bunny-free', '--agent', 'probe', '--format', 'json', '--title', 'local-probe', 'Reply OK'])
  if args.scenario=='continuation':
   events=[json.loads(line) for line in (ROOT/'run-minimal-fake.stdout').read_text().splitlines() if line.startswith('{')]
   sessions={event['sessionID'] for event in events if 'sessionID' in event}
   if len(sessions)!=1:raise RuntimeError('Expected one session in official output')
   run('run-resumed-fake',['run','--standalone','--session',sessions.pop(),'--model','opencode/space-bunny-free','--agent','probe','--format','json','Reply OK again'])
  for label in (['run-minimal-fake','run-resumed-fake'] if args.scenario=='continuation' else ['run-minimal-fake']):
   events=[json.loads(line) for line in (ROOT/(label+'.stdout')).read_text().splitlines() if line.startswith('{')]
   if any(event.get('type')=='error' for event in events):raise RuntimeError('Official emitted an error event: '+label)
   if not any(event.get('type')=='text' and event.get('part',{}).get('text')=='OK' for event in events):raise RuntimeError('Official did not consume terminal OK text: '+label)
  posts=[x for x in CAP if x['method']=='POST']
  if args.scenario=='continuation':
   for header in ['x-opencode-session','x-opencode-session-id','x-session-affinity','x-session-id']:
    values=[{k.lower():v for k,v in post['headers'].items()}.get(header) for post in posts]
    if len(values)!=2 or not values[0] or values[0]!=values[1]:raise RuntimeError('Session continuity mismatch: '+header)
   messages=posts[-1]['body'].get('messages',[])
   if not any(m.get('role')=='assistant' and m.get('content')=='OK' for m in messages):raise RuntimeError('Resumed request lost assistant history')
  if args.scenario in ('tool','tool-error'):
   result=tool_result(args.protocol, posts[-1]['body'])
   if args.scenario=='tool' and 'Synthetic tool fixture only.' not in result:raise RuntimeError('Tool did not read the synthetic file')
   if args.scenario=='tool-error' and 'tool.execution' not in result:raise RuntimeError('Expected synthetic tool argument error')
  (ROOT/'experiment.json').write_text(json.dumps({'protocol':args.protocol,'scenario':args.scenario,'official_sha256':expected,'fake_only':True,'post_count':len(posts),'proxy_attempts':len(PROXY)},indent=2))
  print('CAPTURES', len(CAP), 'OUTPUT', str(ROOT), flush=True)
  if len(posts) != (1 if args.scenario=='text' else 2) or PROXY:
   raise RuntimeError('Unexpected request count or blocked external attempt: inspect fake-only capture before accepting results')
 finally:
  server.shutdown()
  proxy.shutdown()
