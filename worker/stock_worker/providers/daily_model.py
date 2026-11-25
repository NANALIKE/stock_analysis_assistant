"""BaoStock daily field contract and lossless normalization."""

from datetime import date
from decimal import Decimal, InvalidOperation, localcontext
from typing import Any

from .reference_model import symbol

DAILY_FIELDS = (
    "date",
    "code",
    "open",
    "high",
    "low",
    "close",
    "preclose",
    "volume",
    "amount",
    "adjustflag",
    "turn",
    "tradestatus",
    "pctChg",
    "peTTM",
    "pbMRQ",
    "psTTM",
    "pcfNcfTTM",
    "isST",
)

FACTOR_FIELDS = (
    "code",
    "dividOperateDate",
    "foreAdjustFactor",
    "backAdjustFactor",
    "adjustFactor",
)

INT64_MAX = (1 << 63) - 1


def _decimal(value: str, *, nullable: bool = False) -> Decimal | None:
    if value == "" and nullable:
        return None
    try:
        number = Decimal(value)
    except InvalidOperation as error:
        raise ValueError("invalid decimal") from error
    if not number.is_finite():
        raise ValueError("nonfinite decimal")
    return number


def _scaled(value: str, scale: int, *, nullable: bool = False) -> int | None:
    number = _decimal(value, nullable=nullable)
    if number is None:
        return None
    with localcontext() as context:
        context.prec = 50
        scaled = number * scale
    if scaled != scaled.to_integral_value() or scaled < 0 or scaled > INT64_MAX:
        raise ValueError("value exceeds fixed precision or integer range")
    return int(scaled)


def _fraction(value: str) -> float | None:
    number = _decimal(value, nullable=True)
    return None if number is None else float(number / 100)


def _ratio(value: str) -> float | None:
    number = _decimal(value, nullable=True)
    return None if number is None else float(number)


def normalize_daily_record(raw: dict[str, str]) -> dict[str, Any]:
    """Keep every source field, including unknown fields, beside typed records.

    OHLC/amount are 1/10000 CNY integers. Rates are fractions. Missing optional
    numeric values are None, never zero. Identity must match a recognized A-share code.
    """
    if any(field not in raw for field in DAILY_FIELDS):
        raise ValueError("incomplete daily field envelope")
    if any(not isinstance(value, str) for value in raw.values()):
        raise ValueError("source fields must retain SDK strings")
    trade_date = raw["date"]
    date.fromisoformat(trade_date)
    if len(trade_date) != 10:
        raise ValueError("trade date must use YYYY-MM-DD")
    exchange, separator, code = raw["code"].partition(".")
    if separator != "." or exchange not in ("sh", "sz") or len(code) != 6 or not code.isdigit():
        raise ValueError("unsupported BaoStock code")
    identity = symbol(raw["code"])
    if raw["adjustflag"] != "3":
        raise ValueError("base daily records must be unadjusted")
    if raw["tradestatus"] not in ("0", "1") or raw["isST"] not in ("", "0", "1"):
        raise ValueError("invalid daily status")
    valuation = {
        "pe_ttm": _ratio(raw["peTTM"]),
        "pb_mrq": _ratio(raw["pbMRQ"]),
        "ps_ttm": _ratio(raw["psTTM"]),
        "pcf_ncf_ttm": _ratio(raw["pcfNcfTTM"]),
    }
    required = ("open", "high", "low", "close", "volume", "amount")
    if raw["tradestatus"] == "0" and any(raw[field] == "" for field in required):
        # A source-confirmed suspended placeholder supplies status, not a bar.
        # Nonempty mandatory values must still satisfy the numeric contract.
        for field in required:
            if raw[field]:
                _scaled(raw[field], 1 if field == "volume" else 10000)
        return {
            "kind": "daily_status",
            "symbol": identity,
            "trade_date": trade_date,
            "trade_status": "suspended",
            "is_st": None if raw["isST"] == "" else raw["isST"] == "1",
            "valuation": valuation,
            "source_record": dict(raw),
        }
    bar = {
        "trade_date": trade_date,
        "adjustment": "none",
        "open": _scaled(raw["open"], 10000),
        "high": _scaled(raw["high"], 10000),
        "low": _scaled(raw["low"], 10000),
        "close": _scaled(raw["close"], 10000),
        "previous_close": _scaled(raw["preclose"], 10000, nullable=True),
        "volume": _scaled(raw["volume"], 1),
        "amount": _scaled(raw["amount"], 10000),
        "turnover_rate": _fraction(raw["turn"]),
        "change_rate": _fraction(raw["pctChg"]),
        "trade_status": "trading" if raw["tradestatus"] == "1" else "suspended",
        "is_st": None if raw["isST"] == "" else raw["isST"] == "1",
    }
    if not (
        bar["low"]
        <= min(bar["open"], bar["close"])
        <= max(bar["open"], bar["close"])
        <= bar["high"]
    ):
        raise ValueError("invalid OHLC ordering")
    if bar["turnover_rate"] is not None and bar["turnover_rate"] < 0:
        raise ValueError("negative turnover")
    if bar["trade_status"] == "trading" and bar["low"] == 0:
        raise ValueError("trading price must be positive")
    return {
        "symbol": identity,
        "bar": bar,
        "valuation": valuation,
        "source_record": dict(raw),
    }
