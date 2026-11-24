import hashlib
import json
from collections.abc import Iterable
from typing import Any, TextIO

from stock_worker import __version__

from .schema import Request


def encode(value: dict[str, Any]) -> bytes:
    return (
        json.dumps(
            value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False
        )
        + "\n"
    ).encode("utf-8")


def write_batch(
    output: TextIO,
    request: Request,
    records: Iterable[dict[str, Any]],
    lock_sha256: str,
) -> None:
    """Hash exact UTF-8 record envelope bytes, including their trailing LF."""
    output.write(
        encode(
            {
                "type": "begin",
                "schema_version": request.schema_version,
                "run_id": request.run_id,
                "worker_version": __version__,
                "dependency_lock_sha256": lock_sha256,
                "adapter_version": "baostock-unadmitted-v0",
            }
        ).decode("utf-8")
    )
    digest = hashlib.sha256()
    count = 0
    for record in records:
        line = encode({"type": "record", "data": record})
        digest.update(line)
        output.write(line.decode("utf-8"))
        count += 1
    output.write(
        encode({"type": "end", "record_count": count, "sha256": digest.hexdigest()}).decode("utf-8")
    )
    output.flush()
