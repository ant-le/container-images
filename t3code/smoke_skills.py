"""Run only inside the disposable, credential-free image smoke container."""
import json
import os
from pathlib import Path
import selectors
import subprocess
import time


def call(tool, arguments):
    requests = [
        {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
         'params': {'protocolVersion': '2025-06-18', 'capabilities': {},
                    'clientInfo': {'name': 'smoke', 'version': '1'}}},
        {'jsonrpc': '2.0', 'method': 'notifications/initialized'},
        {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call',
         'params': {'name': tool, 'arguments': arguments}},
    ]
    process = subprocess.run(['/usr/bin/python3', '-I', '/opt/t3/skills_mcp.py'],
                             input='\n'.join(map(json.dumps, requests)) + '\n',
                             capture_output=True, text=True, check=True, timeout=10)
    replies = [json.loads(line) for line in process.stdout.splitlines()]
    response = next(reply['result'] for reply in replies if reply.get('id') == 2)
    assert not response.get('isError'), 'Skill tool failed (details withheld)'
    return json.loads(response['content'][0]['text'])


saved = call('save_skill', {'name': 'smoke', 'description': 'Smoke test only',
                          'instructions': 'Reply with the word SMOKE when explicitly invoked.'})
assert saved['complete'] and saved['installed'] == ['codex', 'claude']
# New server process: verifies persistence rather than same-process readback.
assert call('get_skill', {'name': 'smoke'})['revision'] == saved['revision']
assert Path('/home/t3/.agents/skills/t3-user-smoke/SKILL.md').read_bytes() == Path('/home/t3/.claude/skills/t3-user-smoke/SKILL.md').read_bytes()
# These commands inspect only a non-secret MCP entry in a disposable home.
codex = subprocess.run(['codex', 'mcp', 'get', 't3-skills', '--json'],
                       capture_output=True, text=True, check=True, timeout=20)
config = json.loads(codex.stdout)
assert config['transport']['command'] == '/usr/bin/python3'
assert config['transport']['args'] == ['-I', '/opt/t3/skills_mcp.py']
claude = subprocess.run(['claude', 'mcp', 'get', 't3-skills'],
                        capture_output=True, text=True, check=True, timeout=30)
assert '/opt/t3/skills_mcp.py' in claude.stdout
def has_skill(value):
    if isinstance(value, dict):
        return value.get('name') == 't3-user-smoke' or any(has_skill(item) for item in value.values())
    if isinstance(value, list):
        return any(has_skill(item) for item in value)
    return False


def discover(command, messages, is_reply):
    # Native initialization/skills-list only: no prompt, model request or login.
    with subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, cwd='/home/t3/projects') as process:
        selector = selectors.DefaultSelector()
        try:
            process.stdin.write(('\n'.join(map(json.dumps, messages)) + '\n').encode())
            process.stdin.flush()
            selector.register(process.stdout, selectors.EVENT_READ)
            deadline = time.monotonic() + 30
            pending = b''
            while time.monotonic() < deadline:
                if not selector.select(1):
                    continue
                chunk = os.read(process.stdout.fileno(), 65536)
                if not chunk:
                    break
                pending += chunk
                while b'\n' in pending:
                    line, pending = pending.split(b'\n', 1)
                    reply = json.loads(line)
                    if is_reply(reply):
                        assert has_skill(reply), 'Native provider did not discover personal skill'
                        return
            raise AssertionError('Native discovery initialization did not complete')
        finally:
            selector.close()
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


discover(['codex', 'app-server'], [
    {'id': 1, 'method': 'initialize', 'params': {'clientInfo': {'name': 'skills-smoke', 'version': '1'}, 'capabilities': {}}},
    {'method': 'initialized'},
    {'id': 2, 'method': 'skills/list', 'params': {'cwds': ['/home/t3/projects'], 'forceReload': True}},
], lambda reply: reply.get('id') == 2)
discover(['claude', '-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose'], [
    {'type': 'control_request', 'request_id': 'skills-smoke',
     'request': {'subtype': 'initialize', 'hooks': {}, 'sdkMcpServers': []}},
], lambda reply: reply.get('type') == 'control_response'
         and reply.get('response', {}).get('request_id') == 'skills-smoke')
print('PASS: personal skill persistence, MCP registration and native discovery for both providers (no model turn)')
