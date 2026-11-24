import hashlib
import io
import json
import subprocess
import sys
import unittest

from stock_worker.protocol import Request, write_batch


def request(operation="protocol_check"):
    return {
        "schema_version": 1,
        "run_id": "test_run",
        "operation": operation,
        "provider": "baostock",
        "as_of": "2026-11-19",
        "params": {},
    }


class ProtocolTests(unittest.TestCase):
    def test_record_bytes_match_summary(self):
        output = io.StringIO()
        write_batch(output, Request.parse(request()), [{"name": "平安银行"}], "0" * 64)
        lines = output.getvalue().encode().splitlines(keepends=True)
        summary = json.loads(lines[-1])
        self.assertEqual(summary["record_count"], 1)
        self.assertEqual(summary["sha256"], hashlib.sha256(lines[1]).hexdigest())

    def test_invalid_version_and_fields(self):
        for patch in ({"schema_version": 2}, {"schema_version": True}, {"params": None}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                Request.parse(request() | patch)
        with self.assertRaises(ValueError):
            Request.parse(request() | {"unknown": 1})

    def test_protocol_check_and_collect_failure(self):
        for operation in ("protocol_check", "collect"):
            result = subprocess.run(
                [sys.executable, "-m", "stock_worker"],
                input=json.dumps(request(operation)),
                capture_output=True,
                text=True,
                check=False,
            )
            if operation == "collect":
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")
                self.assertEqual(json.loads(result.stderr)["code"], "WORKER_ERROR")
            else:
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, "")
                self.assertEqual(len(result.stdout.splitlines()), 2)
                self.assertEqual(json.loads(result.stdout.splitlines()[-1])["record_count"], 0)
