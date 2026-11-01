#!/usr/bin/env python3
"""Credential-free, stdio-only MCP service for instruction-only personal skills.

The image owns this program. No caller-supplied paths, subprocesses, network,
plugin manifests, raw frontmatter, or executable supporting files are accepted.
"""
import contextlib
import fcntl
import hashlib
import json
import os
import re
import stat
import sys
import uuid

HOME = '/home/t3'
PREFIX = 't3-user-'
MAX_SKILLS = 100
MAX_MESSAGE = 131072
MAX_TEXT = 24000
PROTOCOLS = ('2024-11-05', '2025-03-26', '2025-06-18')


class Rejected(Exception):
    pass


def name_check(name):
    if not isinstance(name, str) or not re.fullmatch(r'[a-z][a-z0-9-]{0,39}', name):
        raise Rejected('Use a lowercase skill name of 1–40 letters, digits or hyphens, starting with a letter.')
    return name


def text_check(value, limit):
    if not isinstance(value, str) or not value.strip() or len(value.encode('utf-8')) > limit:
        raise Rejected('Skill text is empty or exceeds its size limit.')
    if any(ord(c) < 32 and c not in '\n\t' for c in value):
        raise Rejected('Control characters are not allowed.')
    # Claude evaluates this syntax before sending skill content to the model.
    if re.search(r'!\s*`', value):
        raise Rejected('Dynamic command expansion is not allowed in personal skills.')
    return value


def document(name, description, instructions):
    name_check(name)
    text_check(description, 500)
    text_check(instructions, MAX_TEXT)
    # JSON quoted strings are YAML-compatible. Users cannot inject frontmatter
    # keys (hooks, allowed-tools, context, MCP, etc.) through these fields.
    return ('---\nname: ' + json.dumps(PREFIX + name) + '\ndescription: '
            + json.dumps(description, ensure_ascii=True) + '\n---\n\n'
            + instructions + '\n').encode('utf-8')


@contextlib.contextmanager
def directory(path):
    """Walk from / using directory descriptors; never follow any symlink."""
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in str(path).split('/'):
            if not part:
                continue
            if part in ('.', '..'):
                raise Rejected('Unsafe storage path.')
            try:
                os.mkdir(part, 0o700, dir_fd=fd)
            except FileExistsError:
                pass
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        yield fd
    finally:
        os.close(fd)


def read_record(fd, name):
    try:
        file_fd = os.open(name + '.json', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    except FileNotFoundError:
        return None
    with os.fdopen(file_fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > MAX_MESSAGE:
            raise Rejected('Unsafe skill record.')
        try:
            value = json.loads(stream.read(MAX_MESSAGE + 1))
            if not isinstance(value, dict) or set(value) != {'name', 'description', 'instructions'} or value['name'] != name:
                raise ValueError()
            document(**value)
        except (ValueError, TypeError, KeyError):
            raise Rejected('Invalid skill record.') from None
    return value


def replace_file(fd, name, content):
    # Never open an existing destination for writing (including hard links).
    tmp = '.new-' + uuid.uuid4().hex
    file_fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=fd)
    try:
        with os.fdopen(file_fd, 'wb') as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, name, src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
    finally:
        try:
            os.unlink(tmp, dir_fd=fd)
        except FileNotFoundError:
            pass


class Skills:
    # Tests use a disposable root. The protocol never accepts or overrides home.
    def __init__(self, home=HOME):
        self.home = home

    @contextlib.contextmanager
    def locked(self):
        with directory(self.home + '/.t3-skills') as fd:
            lock = os.open('.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=fd)
            try:
                info = os.fstat(lock)
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                    raise Rejected('Unsafe skill lock.')
                fcntl.flock(lock, fcntl.LOCK_EX)
                yield fd
            finally:
                os.close(lock)

    def names(self, fd):
        names = []
        for entry in os.listdir(fd):
            if entry.endswith('.json'):
                name_check(entry[:-5])
                names.append(entry[:-5])
                if len(names) > MAX_SKILLS:
                    raise Rejected('Personal skill limit exceeded.')
        return sorted(names)

    def call(self, tool, args):
        if not isinstance(args, dict):
            raise Rejected('Arguments must be an object.')
        if tool == 'list_skills':
            if args:
                raise Rejected('list_skills accepts no arguments.')
            with self.locked() as fd:
                return {'names': self.names(fd), 'prefix': PREFIX}
        if tool == 'get_skill':
            if set(args) != {'name'}:
                raise Rejected('get_skill accepts only name.')
            name = name_check(args['name'])
            with self.locked() as fd:
                record = read_record(fd, name)
                if record is None:
                    raise Rejected('Skill does not exist.')
                return {**record, 'revision': hashlib.sha256(document(**record)).hexdigest()}
        if tool != 'save_skill':
            raise Rejected('Unknown tool.')
        if not {'name', 'description', 'instructions'} <= args.keys() or args.keys() - {'name', 'description', 'instructions', 'expected_revision'}:
            raise Rejected('save_skill accepts name, description, instructions and optional expected_revision only.')
        record = {key: args[key] for key in ('name', 'description', 'instructions')}
        content = document(**record)
        revision = hashlib.sha256(content).hexdigest()
        expected = args.get('expected_revision')
        if expected is not None and (not isinstance(expected, str) or not re.fullmatch('[0-9a-f]{64}', expected)):
            raise Rejected('Invalid expected revision.')
        name = record['name']
        with self.locked() as fd, contextlib.ExitStack() as stack:
            old = read_record(fd, name)
            if old is None:
                if expected is not None:
                    raise Rejected('Skill does not exist; refresh before saving.')
                if len(self.names(fd)) >= MAX_SKILLS:
                    raise Rejected('Personal skill limit reached.')
            else:
                old_revision = hashlib.sha256(document(**old)).hexdigest()
                if old_revision != revision and expected != old_revision:
                    raise Rejected('Skill changed or already exists; get_skill and pass its revision before updating.')
            destinations = []
            for provider, relative in [('codex', '/.agents/skills/'), ('claude', '/.claude/skills/')]:
                dest = stack.enter_context(directory(self.home + relative + PREFIX + name))
                entries = os.listdir(dest)
                if any(entry != 'SKILL.md' for entry in entries):
                    raise Rejected('Skill directory contains unmanaged files; operator review required.')
                if 'SKILL.md' in entries:
                    info = os.stat('SKILL.md', dir_fd=dest, follow_symlinks=False)
                    if old is None or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                        raise Rejected('Refusing to overwrite an unmanaged or linked skill file.')
                destinations.append((provider, dest))
            # Commit the bounded record before mirrors so an interrupted install
            # is repairable by retrying the identical save. No input is executed.
            replace_file(fd, name + '.json', json.dumps(record).encode('utf-8'))
            installed = []
            for provider, dest in destinations:
                try:
                    replace_file(dest, 'SKILL.md', content)
                except OSError:
                    return {'revision': revision, 'installed': installed, 'complete': False,
                            'next': 'Record saved; installation incomplete. Retry the identical save_skill call.'}
                installed.append(provider)
            return {'name': PREFIX + name, 'revision': revision, 'installed': installed,
                    'complete': True, 'next': 'Restart agent session in T3, then select $' + PREFIX + name
                    + '. File installation alone does not prove provider discovery.'}


TOOLS = [
    {'name': 'save_skill', 'description': 'Create an instruction-only personal skill for BOTH Codex and Claude on Hetzner. Use when asked to create or maintain a skill. No secrets, hooks, scripts, plugins or connections. For updates call get_skill and supply expected_revision. Skills are global across this environment; restart the agent session after saving.',
     'inputSchema': {'type': 'object', 'properties': {
         'name': {'type': 'string', 'pattern': '^[a-z][a-z0-9-]{0,39}$'},
         'description': {'type': 'string', 'maxLength': 500},
         'instructions': {'type': 'string', 'maxLength': MAX_TEXT},
         'expected_revision': {'type': 'string', 'pattern': '^[0-9a-f]{64}$'}},
         'required': ['name', 'description', 'instructions'], 'additionalProperties': False}},
    {'name': 'get_skill', 'description': 'Read a personal skill previously saved by this service and its revision. Cannot read other files.',
     'inputSchema': {'type': 'object', 'properties': {'name': {'type': 'string'}}, 'required': ['name'], 'additionalProperties': False}},
    {'name': 'list_skills', 'description': 'List names of personal skills managed by this service.',
     'inputSchema': {'type': 'object', 'properties': {}, 'additionalProperties': False}},
]


def response(request, skills):
    if not isinstance(request, dict) or request.get('jsonrpc') != '2.0' or not isinstance(request.get('method'), str):
        return {'jsonrpc': '2.0', 'id': None, 'error': {'code': -32600, 'message': 'Invalid request'}}
    if 'id' not in request:
        return None
    result = {'jsonrpc': '2.0', 'id': request['id']}
    method = request['method']
    params = request.get('params', {})
    if not isinstance(params, dict):
        return {**result, 'error': {'code': -32602, 'message': 'Invalid parameters'}}
    if method == 'initialize':
        protocol = params.get('protocolVersion')
        result['result'] = {'protocolVersion': protocol if protocol in PROTOCOLS else PROTOCOLS[-1],
                            'capabilities': {'tools': {}}, 'serverInfo': {'name': 't3-personal-skills', 'version': '1.0.0'}}
    elif method == 'ping':
        result['result'] = {}
    elif method == 'tools/list':
        result['result'] = {'tools': TOOLS}
    elif method == 'tools/call':
        try:
            value = skills.call(params.get('name'), params.get('arguments', {}))
            result['result'] = {'content': [{'type': 'text', 'text': json.dumps(value)}],
                                'isError': value.get('complete') is False}
        except Rejected as error:
            result['result'] = {'content': [{'type': 'text', 'text': str(error)}], 'isError': True}
        except (OSError, ValueError, TypeError, UnicodeError):
            # Never echo filesystem contents, input text, environment or tracebacks.
            result['result'] = {'content': [{'type': 'text', 'text': 'Skill storage unavailable or unsafe. No credential access is needed; ask the operator to inspect storage.'}], 'isError': True}
    else:
        result['error'] = {'code': -32601, 'message': 'Method not found'}
    return result


def main():
    os.environ.clear()
    os.umask(0o077)
    if sys.argv[1:] == ['--configure']:
        try:
            configure()
        except (Rejected, OSError, ValueError, TypeError):
            raise SystemExit('Skill tool registration failed; operator review required. Existing configuration was not intentionally replaced.') from None
        return
    if sys.argv[1:]:
        raise SystemExit('Unsupported arguments')
    skills = Skills()
    while True:
        raw = sys.stdin.buffer.readline(MAX_MESSAGE + 1)
        if not raw:
            return
        if len(raw) > MAX_MESSAGE:
            return  # Bound memory; terminate on oversized framing without echo.
        try:
            request = json.loads(raw)
            reply = response(request, skills)
        except (ValueError, UnicodeError):
            reply = {'jsonrpc': '2.0', 'id': None, 'error': {'code': -32700, 'message': 'Parse error'}}
        if reply is not None:
            print(json.dumps(reply), flush=True)


def configure(home=HOME):
    """Startup-only registration; preserve all unrelated Claude configuration.

    CLAUDE_CONFIG_DIR is fixed to /home/t3/.claude in this image. Codex's
    registration is image-owned /etc/codex/config.toml instead.
    """
    entry = {'type': 'stdio', 'command': '/usr/bin/python3',
             'args': ['-I', '/opt/t3/skills_mcp.py']}
    with directory(home + '/.claude') as fd:
        try:
            source = os.open('.claude.json', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
        except FileNotFoundError:
            config = {}
        else:
            with os.fdopen(source, 'rb') as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 4 * 1024 * 1024:
                    raise Rejected('Unsafe Claude configuration; operator review required.')
                config = json.load(stream)
        if not isinstance(config, dict) or not isinstance(config.get('mcpServers', {}), dict):
            raise Rejected('Invalid Claude configuration; operator review required.')
        servers = config.setdefault('mcpServers', {})
        if 't3-skills' in servers:
            if servers['t3-skills'] != entry:
                raise Rejected('Existing t3-skills configuration conflicts; operator review required.')
            return
        servers['t3-skills'] = entry
        replace_file(fd, '.claude.json', json.dumps(config, indent=2).encode('utf-8'))


if __name__ == '__main__':
    main()
