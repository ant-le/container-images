import hashlib
import io
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import staging


class StagingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = str(Path(self.temp.name).resolve() / "stage")
        self.key = "a" * 32

    def call(self, op, data=b"", key=None):
        out = io.BytesIO()
        staging.run(op, key or self.key, io.BytesIO(data), out, self.root)
        return out.getvalue()

    def test_roundtrip_and_release(self):
        data = bytes(range(256)) * 8192
        result = self.call("put", data)
        self.assertIn(hashlib.sha256(data).hexdigest().encode(), result)
        Path(self.root, self.key + ".output").write_bytes(data[::-1])
        self.assertEqual(self.call("get"), data[::-1])
        self.call("release")
        self.assertEqual(os.listdir(self.root), [])

    def test_roundtrip_when_filesystem_root_is_not_readable(self):
        real_open = os.open

        def confined_open(path, *args, **kwargs):
            if path == "/":
                raise PermissionError("filesystem root is outside the sandbox policy")
            return real_open(path, *args, **kwargs)

        with patch("staging.os.open", side_effect=confined_open):
            self.call("put", b"synthetic input")
            Path(self.root, self.key + ".output").write_bytes(b"synthetic output")
            self.assertEqual(self.call("get"), b"synthetic output")
            self.call("release")
        self.assertEqual(os.listdir(self.root), [])

    def test_reject_links_and_special_files(self):
        self.call("reserve")
        out = Path(self.root, self.key + ".output")
        secret = Path(self.temp.name, "secret")
        secret.write_text("must not read")
        out.symlink_to(secret)
        with self.assertRaises(OSError):
            self.call("get")
        out.unlink()
        os.link(secret, out)
        with self.assertRaises(ValueError):
            self.call("get")
        out.unlink()
        os.mkfifo(out)
        with self.assertRaises(ValueError):
            self.call("get")

    def test_reject_root_symlink_and_traversal(self):
        Path(self.root).symlink_to(self.temp.name, target_is_directory=True)
        with self.assertRaises(OSError):
            self.call("reserve")
        with self.assertRaises(ValueError):
            self.call("get", key="../secret")

    def test_limit_and_interrupted_copy_cleanup(self):
        class Broken:
            def read(self, _):
                raise OSError("interrupted")

        with self.assertRaises(OSError):
            staging.run("put", self.key, Broken(), io.BytesIO(), self.root)
        self.assertEqual(os.listdir(self.root), [])
        old = staging.MAX_FILE
        try:
            staging.MAX_FILE = 8
            with self.assertRaises(ValueError):
                self.call("put", b"123456789")
            self.assertEqual(os.listdir(self.root), [])
        finally:
            staging.MAX_FILE = old

    def test_exclusive_and_capacity(self):
        self.call("reserve")
        with self.assertRaises(FileExistsError):
            self.call("reserve")
        for i in range(staging.MAX_ENTRIES):
            Path(self.root, str(i)).touch()
        with self.assertRaises(ValueError):
            self.call("reserve", key="b" * 32)


if __name__ == "__main__":
    unittest.main()
