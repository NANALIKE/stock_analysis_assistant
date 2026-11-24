import hashlib
import json
import sys
from pathlib import Path

from stock_worker.protocol import Request, write_batch
from stock_worker.providers.baostock import collect


def main() -> int:
    try:
        request = Request.parse(json.load(sys.stdin))
        records = [] if request.operation == "protocol_check" else collect(request)
        lock = Path(__file__).resolve().parent / "dependencies.lock"
        lock_hash = hashlib.sha256(lock.read_bytes()).hexdigest()
        write_batch(sys.stdout, request, records, lock_hash)
        return 0
    except (ValueError, TypeError, RuntimeError, OSError) as error:
        sys.stderr.write(json.dumps({"code": "WORKER_ERROR", "message": str(error)}) + "\n")
        return 1


if __name__ == "__main__":
    sys.exit(main())
