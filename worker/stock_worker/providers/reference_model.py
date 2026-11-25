"""Reference data conversion, with explicit exclusions from the A-share universe."""

from datetime import date
from typing import Any

PREFIXES = {
    "sh": ("600", "601", "603", "605", "688", "689"),
    "sz": ("000", "001", "002", "003", "300", "301"),
}


def symbol(code: str) -> str:
    exchange, separator, number = code.partition(".")
    if (
        separator != "."
        or exchange not in PREFIXES
        or len(number) != 6
        or not number.isdigit()
        or not number.startswith(PREFIXES[exchange])
    ):
        raise ValueError("EXCLUDED_SECURITY: unrecognized A-share code")
    return f"{number}.{exchange.upper()}"


def iso(value: str, *, nullable: bool = False) -> str | None:
    if not value and nullable:
        return None
    date.fromisoformat(value)
    if len(value) != 10:
        raise ValueError("date must use YYYY-MM-DD")
    return value


def normalize_reference(operation: str, raw: dict[str, str]) -> dict[str, Any]:
    if operation == "trade_dates":
        if raw["is_trading_day"] not in ("0", "1"):
            raise ValueError("invalid calendar flag")
        return {
            "kind": "calendar",
            "trade_date": iso(raw["calendar_date"]),
            "is_open": raw["is_trading_day"] == "1",
            "source_record": raw,
        }
    identity = symbol(raw["code"])
    if operation == "stock_basic":
        if raw["type"] != "1":
            raise ValueError("EXCLUDED_SECURITY: security type is not stock")
        if raw["status"] not in ("0", "1"):
            raise ValueError("invalid listing status")
        return {
            "kind": "instrument",
            "symbol": identity,
            "name": raw["code_name"],
            "exchange": identity[-2:],
            "listed_on": iso(raw["ipoDate"]),
            "delisted_on": iso(raw["outDate"], nullable=True),
            "status": "listed" if raw["status"] == "1" else "delisted",
            "source_record": raw,
        }
    if raw["tradeStatus"] not in ("0", "1"):
        raise ValueError("invalid market status")
    return {
        "kind": "status",
        "symbol": identity,
        "name": raw["code_name"],
        "trade_status": "trading" if raw["tradeStatus"] == "1" else "suspended",
        "source_record": raw,
    }
