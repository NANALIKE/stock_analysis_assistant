from collections.abc import Iterable
from typing import Any

from stock_worker.protocol import Request

from .response import normalize, response, validate_queries
from .session import session


def collect(request: Request) -> Iterable[dict[str, Any]]:
    queries = validate_queries(request.params)
    if "replay" in request.params:
        saved = dict(request.params["replay"])
        saved["records"], saved["issues"] = normalize(
            saved["operation"], saved["fields"], saved["rows"]
        )
        yield saved
        return
    with session() as sdk:
        for query in queries:
            yield response(sdk, query)
