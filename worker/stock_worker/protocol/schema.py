from dataclasses import dataclass
from datetime import date
from typing import Any

SCHEMA_VERSION = 1


@dataclass(frozen=True)
class Request:
    schema_version: int
    run_id: str
    operation: str
    provider: str
    as_of: str
    params: dict[str, Any]

    @classmethod
    def parse(cls, value: dict[str, Any]) -> "Request":
        if not isinstance(value, dict):
            raise ValueError("request must be a JSON object")
        if set(value) != set(cls.__dataclass_fields__):
            raise ValueError("request fields do not match schema v1")
        if type(value["schema_version"]) is not int or value["schema_version"] != SCHEMA_VERSION:
            raise ValueError("unsupported schema_version")
        for field in ("run_id", "operation", "provider", "as_of"):
            if not isinstance(value[field], str) or not value[field]:
                raise ValueError(f"{field} must be a nonempty string")
        date.fromisoformat(value["as_of"])
        if len(value["as_of"]) != 10:
            raise ValueError("as_of must use YYYY-MM-DD")
        if not isinstance(value["params"], dict):
            raise ValueError("params must be an object")
        if value["operation"] not in ("protocol_check", "collect"):
            raise ValueError("unsupported operation")
        if value["provider"] != "baostock":
            raise ValueError("unsupported provider")
        return cls(**value)
