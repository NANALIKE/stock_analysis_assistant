import io
import unittest
from contextlib import redirect_stderr, redirect_stdout
from types import SimpleNamespace
from unittest.mock import patch

from stock_worker.providers.session import consume, session


class PagedResult:
    fields = ["code"]
    error_code = "0"
    error_msg = ""

    def __init__(self, fail=False):
        self.index = 0
        self.fail = fail

    def next(self):
        self.index += 1
        print("SDK page")
        if self.index == 3 and self.fail:
            self.error_code = "source-error"
            self.error_msg = "page failed"
        return self.index <= 2

    def get_row_data(self):
        return [str(self.index)]


class SessionTests(unittest.TestCase):
    def test_all_pages_and_final_error(self):
        with redirect_stdout(io.StringIO()):
            fields, rows, code, _, complete = consume(PagedResult())
            self.assertEqual((fields, rows, code, complete), (["code"], [["1"], ["2"]], "0", True))
            _, rows, code, _, complete = consume(PagedResult(fail=True))
            self.assertEqual(rows, [["1"], ["2"]])
            self.assertEqual(code, "source-error")
            self.assertFalse(complete)

    def test_stdout_and_finally_logout(self):
        closed = []

        def login():
            print("SDK login")
            return SimpleNamespace(error_code="0")

        def logout():
            print("SDK logout")
            closed.append(True)

        sdk = SimpleNamespace(login=login, logout=logout)
        output, error = io.StringIO(), io.StringIO()
        with (
            patch("stock_worker.providers.session.importlib.import_module", return_value=sdk),
            redirect_stdout(output),
            redirect_stderr(error),
            self.assertRaisesRegex(RuntimeError, "interrupted"),
        ):
            with session():
                consume(PagedResult())
                raise RuntimeError("interrupted")
        self.assertEqual(output.getvalue(), "")
        self.assertIn("SDK page", error.getvalue())
        self.assertIn("SDK logout", error.getvalue())
        self.assertEqual(closed, [True])
