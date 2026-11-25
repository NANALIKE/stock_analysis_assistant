import hashlib
import importlib.metadata
import json
import sys
from pathlib import Path

from stock_worker.protocol import Request, write_batch
from stock_worker.providers.baostock import collect
from stock_worker.providers.response import validate_queries


def main() -> int:
    try:
        request = Request.parse(json.load(sys.stdin))
        if request.operation == "collect":
            validate_queries(request.params)
        lock = Path(__file__).resolve().parent / "dependencies.lock"
        if request.operation == "protocol_check" and request.params.get("check_dependencies"):
            for line in lock.read_text().splitlines():
                if line and not line.startswith("#"):
                    name, version = line.split("==")
                    try:
                        installed = importlib.metadata.version(name)
                    except importlib.metadata.PackageNotFoundError as error:
                        raise RuntimeError(f"SOURCE_UNAVAILABLE: missing {name}") from error
                    if installed != version:
                        raise RuntimeError(f"SOURCE_UNAVAILABLE: {name} version differs from lock")
        records = [] if request.operation == "protocol_check" else collect(request)
        lock_hash = hashlib.sha256(lock.read_bytes()).hexdigest()
        write_batch(sys.stdout, request, records, lock_hash)
        return 0
    except (ValueError, TypeError, RuntimeError, OSError) as error:
        sys.stderr.write(json.dumps({"code": "WORKER_ERROR", "message": str(error)}) + "\n")
        return 1


if __name__ == "__main__":
    sys.exit(main())
