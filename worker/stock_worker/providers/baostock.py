from collections.abc import Iterable
from typing import Any

from stock_worker.protocol import Request


def collect(request: Request) -> Iterable[dict[str, Any]]:
    raise RuntimeError("BaoStock provider is not admitted; collection adapter is not implemented")
