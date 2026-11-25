"""One serial SDK session per Worker process."""

import importlib
import sys
from contextlib import contextmanager, redirect_stdout
from typing import Any


@contextmanager
def session():
    # Import, login, iteration and logout can all print to stdout.
    with redirect_stdout(sys.stderr):
        try:
            sdk = importlib.import_module("baostock")
        except ImportError as error:
            raise RuntimeError(
                "SOURCE_UNAVAILABLE: install the locked runtime dependencies"
            ) from error
        try:
            result = sdk.login()
            if result.error_code != "0":
                raise RuntimeError(f"SOURCE_UNAVAILABLE: SDK login code {result.error_code}")
            yield sdk
        finally:
            sdk.logout()


def consume(result: Any) -> tuple[list[str], list[list[str]], str, str, bool]:
    """Consume all pages, including errors discovered by next()."""
    fields = list(result.fields or [])
    rows = []
    while result.error_code == "0" and result.next():
        rows.append(list(result.get_row_data()))
    code, message = str(result.error_code), str(result.error_msg)
    return fields, rows, code, message, code == "0"
