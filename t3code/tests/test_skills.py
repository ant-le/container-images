import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / 'skills_mcp.py'
spec = importlib.util.spec_from_file_location('skills_mcp', SOURCE)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class SkillTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        # macOS /var is a symlink; production deliberately rejects symlink roots.
        self.home = Path(self.tmp.name).resolve()
        self.skills = m.Skills(str(self.home))
        self.args = {'name': 'review', 'description': 'Review changes',
                     'instructions': 'Check the diff and report important findings.'}
        self.secret = self.home / 'credential-fixture'
        self.secret.write_text('DO-NOT-EXPOSE-CREDENTIAL-FIXTURE')

    def tearDown(self):
        self.assertEqual(self.secret.read_text(), 'DO-NOT-EXPOSE-CREDENTIAL-FIXTURE')
        self.tmp.cleanup()

    def save(self, **changes):
        return self.skills.call('save_skill', self.args | changes)

    def test_create_both_providers_and_persist_across_process_objects(self):
        result = self.save()
        self.assertTrue(result['complete'])
        self.assertEqual(result['installed'], ['codex', 'claude'])
        for provider in ['.agents', '.claude']:
            content = (self.home / provider / 'skills/t3-user-review/SKILL.md').read_bytes()
            self.assertEqual(content, m.document(**self.args))
        reopened = m.Skills(str(self.home))
        self.assertEqual(reopened.call('get_skill', {'name': 'review'})['revision'], result['revision'])
        self.assertEqual(reopened.call('list_skills', {})['names'], ['review'])

    def test_updates_require_current_revision_and_identical_retry_is_safe(self):
        first = self.save()
        with self.assertRaises(m.Rejected):
            self.save(instructions='Changed')
        second = self.save(instructions='Changed', expected_revision=first['revision'])
        self.assertNotEqual(first['revision'], second['revision'])
        with self.assertRaises(m.Rejected):
            self.save(instructions='Stale change', expected_revision=first['revision'])
        self.assertEqual(self.save(instructions='Changed')['revision'], second['revision'])

    def test_no_paths_executable_metadata_or_dynamic_commands(self):
        for name in ['../credential-fixture', '/home/t3/.codex/auth.json', '..', 'a/b', 'a\\b', '', 'A', 'a' * 41]:
            with self.subTest(name=name), self.assertRaises(m.Rejected):
                self.save(name=name)
        for extra in ['path', 'command', 'hooks', 'allowed-tools', 'files', 'mcpServers', 'env', 'home']:
            with self.subTest(extra=extra), self.assertRaises(m.Rejected):
                self.save(**{extra: 'forbidden'})
        for text in ['!`cat secret`', '! \n`cat secret`', 'x\x00y', 'x' * 24001]:
            with self.subTest(text=text[:20]), self.assertRaises(m.Rejected):
                self.save(instructions=text)
        with self.assertRaises(m.Rejected):
            self.save(description='!`command`')

    def test_description_cannot_inject_yaml_fields(self):
        description = 'hello\n---\nhooks:\n  command: bad'
        content = m.document('review', description, 'Ordinary text').decode()
        self.assertEqual(content.splitlines()[2], 'description: ' + json.dumps(description))
        self.assertEqual(content.splitlines()[3], '---')

    def test_all_ancestor_symlinks_rejected(self):
        for prefix in ['.t3-skills', '.agents', '.claude']:
            with self.subTest(prefix=prefix):
                link = self.home / prefix
                target = self.home / ('target-' + prefix)
                target.mkdir()
                link.symlink_to(target, target_is_directory=True)
                try:
                    with self.assertRaises(OSError):
                        self.save()
                finally:
                    link.unlink()
        self.assertFalse((self.home / 'target-.agents/skills').exists())

    def test_existing_skill_symlink_or_hardlink_cannot_expose_or_modify_secret(self):
        self.save()
        dest = self.home / '.agents/skills/t3-user-review/SKILL.md'
        for hard in [False, True]:
            dest.unlink()
            if hard:
                os.link(self.secret, dest)
            else:
                dest.symlink_to(self.secret)
            with self.subTest(hard=hard), self.assertRaises(m.Rejected):
                self.save()

    def test_record_symlink_hardlink_and_invalid_schema_are_not_returned(self):
        self.save()
        record = self.home / '.t3-skills/review.json'
        for kind in ['symlink', 'hardlink', 'invalid']:
            record.unlink()
            if kind == 'symlink':
                record.symlink_to(self.secret)
            elif kind == 'hardlink':
                os.link(self.secret, record)
            else:
                record.write_text(json.dumps({'token': self.secret.read_text()}))
            reply = m.response({'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                'params': {'name': 'get_skill', 'arguments': {'name': 'review'}}}, self.skills)
            self.assertTrue(reply['result']['isError'])
            self.assertNotIn(self.secret.read_text(), json.dumps(reply))

    def test_refuses_unmanaged_skill_and_plugin_files(self):
        dest = self.home / '.claude/skills/t3-user-review'
        dest.mkdir(parents=True)
        (dest / 'SKILL.md').write_text('Existing unrelated skill')
        with self.assertRaises(m.Rejected):
            self.save()
        self.assertEqual((dest / 'SKILL.md').read_text(), 'Existing unrelated skill')
        (dest / 'SKILL.md').unlink()
        (dest / '.claude-plugin').mkdir()
        with self.assertRaises(m.Rejected):
            self.save()

    def test_partial_install_reports_failure_and_identical_retry_repairs(self):
        real = m.replace_file
        count = 0
        def fail_second(fd, name, content):
            nonlocal count
            if name == 'SKILL.md':
                count += 1
                if count == 2:
                    raise OSError('dummy failure')
            return real(fd, name, content)
        with patch.object(m, 'replace_file', fail_second):
            result = self.save()
        self.assertFalse(result['complete'])
        self.assertEqual(result['installed'], ['codex'])
        self.assertTrue(self.save()['complete'])

    def test_limit_bounds_persistent_storage(self):
        with patch.object(m, 'MAX_SKILLS', 1):
            self.save()
            with self.assertRaises(m.Rejected):
                self.save(name='second')
            self.assertTrue(self.save()['complete'])

    def test_registration_preserves_settings_and_refuses_conflicts(self):
        config = self.home / '.claude/.claude.json'
        config.parent.mkdir()
        original = {'projects': {'example': {'trusted': True}}, 'unrelated': 'PRIVATE-FIXTURE',
                    'mcpServers': {'existing': {'type': 'http', 'url': 'https://example.invalid'}}}
        config.write_text(json.dumps(original))
        m.configure(str(self.home))
        first = config.read_bytes()
        m.configure(str(self.home))
        self.assertEqual(first, config.read_bytes())
        parsed = json.loads(first)
        parsed['mcpServers'].pop('t3-skills')
        self.assertEqual(parsed, original)
        parsed['mcpServers']['t3-skills'] = {'command': 'unreviewed'}
        config.write_text(json.dumps(parsed))
        with self.assertRaises(m.Rejected):
            m.configure(str(self.home))
        self.assertEqual(json.loads(config.read_text()), parsed)

    def test_registration_refuses_credential_symlink(self):
        config = self.home / '.claude/.claude.json'
        config.parent.mkdir()
        config.symlink_to(self.secret)
        with self.assertRaises(OSError):
            m.configure(str(self.home))

    def test_stdio_negotiation_discovery_and_malformed_input_without_credentials(self):
        messages = [
            {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize', 'params': {'protocolVersion': '2025-06-18'}},
            {'jsonrpc': '2.0', 'method': 'notifications/initialized'},
            {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list'},
            {'jsonrpc': '2.0', 'id': 3, 'method': 'ping'},
        ]
        result = subprocess.run([sys.executable, '-I', str(SOURCE)],
                                input='\n'.join(map(json.dumps, messages)) + '\nINVALID\n',
                                capture_output=True, text=True, timeout=5,
                                env={'DUMMY_SECRET': 'NEVER-ECHO-ENVIRONMENT'})
        self.assertEqual(result.returncode, 0, result.stderr)
        replies = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertEqual(len(replies), 4)
        self.assertEqual(replies[0]['result']['protocolVersion'], '2025-06-18')
        self.assertEqual({t['name'] for t in replies[1]['result']['tools']}, {'save_skill', 'get_skill', 'list_skills'})
        self.assertEqual(replies[3]['error']['code'], -32700)
        self.assertNotIn('NEVER-ECHO', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
