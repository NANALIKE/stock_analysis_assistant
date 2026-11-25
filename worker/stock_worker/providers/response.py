"""Strict query whitelist and lossless per-query response envelopes."""

import re
from datetime import datetime, timezone
from typing import Any

from .action_model import DIVIDEND_FIELDS, normalize_action
from .daily_model import DAILY_FIELDS, FACTOR_FIELDS, normalize_daily_record
from .reference_model import iso, normalize_reference
from .session import consume

PARAMETERS = {
    "stock_basic": {"code"},
    "trade_dates": {"start_date", "end_date"},
    "all_stock": {"day"},
    "daily_history": {"date"},
    "history": {"code", "start_date", "end_date", "adjustflag"},
    "adjust_factor": {"code", "start_date", "end_date"},
    "daily_adjust_factor": {"date"},
    "dividend": {"code", "year", "year_type"},
}
FIELDS = {
    "stock_basic": ("code", "code_name", "ipoDate", "outDate", "type", "status"),
    "trade_dates": ("calendar_date", "is_trading_day"),
    "all_stock": ("code", "code_name", "tradeStatus"),
    "daily_history": DAILY_FIELDS,
    "history": DAILY_FIELDS,
    "adjust_factor": FACTOR_FIELDS,
    "daily_adjust_factor": FACTOR_FIELDS,
    "dividend": DIVIDEND_FIELDS,
}


def validate_queries(params: dict[str, Any]) -> list[dict[str, Any]]:
    if set(params) not in ({"queries"}, {"queries", "replay"}) or not isinstance(
        params["queries"], list
    ):
        raise ValueError("collect requires queries")
    queries = params["queries"]
    if not 1 <= len(queries) <= 8:
        raise ValueError("batch requires 1..8 queries")
    seen = set()
    for query in queries:
        if not isinstance(query, dict) or set(query) != {"query_id", "operation", "params"}:
            raise ValueError("invalid query shape")
        identity, operation, values = query["query_id"], query["operation"], query["params"]
        if not isinstance(identity, str) or not identity or identity in seen:
            raise ValueError("query_id must be unique and nonempty")
        seen.add(identity)
        if operation not in PARAMETERS or not isinstance(values, dict):
            raise ValueError("unsupported query")
        expected = PARAMETERS[operation]
        if set(values) != expected:
            raise ValueError("query parameters do not match operation")
        for key, value in values.items():
            if not isinstance(value, str):
                raise ValueError("query parameters must be strings")
            if key in ("date", "day", "start_date", "end_date"):
                iso(value)
            if key == "code" and value and not re.fullmatch(r"(sh|sz)\.\d{6}", value):
                raise ValueError("invalid provider code")
        if "code" in values and operation != "stock_basic" and not values["code"]:
            raise ValueError("code is required")
        if "start_date" in values and values["start_date"] > values["end_date"]:
            raise ValueError("inverted query range")
        if operation == "history" and values["adjustflag"] not in ("2", "3"):
            raise ValueError("history adjustflag must be 2 or 3")
        if operation == "dividend":
            if not re.fullmatch(r"\d{4}", values["year"]) or not 1990 <= int(values["year"]):
                raise ValueError("invalid dividend year")
            if values["year_type"] not in ("report", "operate"):
                raise ValueError("invalid dividend year_type")
    if any(q["operation"] in ("daily_history", "history", "all_stock") for q in queries):
        if len(queries) != 1:
            raise ValueError("large queries require their own batch")
    if "replay" in params:
        saved = params["replay"]
        if len(queries) != 1 or not isinstance(saved, dict):
            raise ValueError("replay requires one source response")
        query = queries[0]
        if (
            saved.get("query_id") != query["query_id"]
            or saved.get("operation") != query["operation"]
            or saved.get("request") != query["params"]
            or saved.get("error_code") != "0"
            or saved.get("complete") is not True
        ):
            raise ValueError("invalid replay response")
        if not isinstance(saved.get("fields"), list) or not isinstance(saved.get("rows"), list):
            raise ValueError("replay requires original fields and rows")
    return queries


def invoke(sdk: Any, operation: str, p: dict[str, str]):
    if operation == "stock_basic":
        return sdk.query_stock_basic(code=p["code"])
    if operation == "trade_dates":
        return sdk.query_trade_dates(start_date=p["start_date"], end_date=p["end_date"])
    if operation == "all_stock":
        return sdk.query_all_stock(day=p["day"])
    if operation == "daily_history":
        return sdk.query_daily_history_k_AStock(date=p["date"])
    if operation == "history":
        return sdk.query_history_k_data_plus(
            p["code"],
            ",".join(DAILY_FIELDS),
            start_date=p["start_date"],
            end_date=p["end_date"],
            frequency="d",
            adjustflag=p["adjustflag"],
        )
    if operation == "adjust_factor":
        return sdk.query_adjust_factor(
            p["code"], start_date=p["start_date"], end_date=p["end_date"]
        )
    if operation == "daily_adjust_factor":
        return sdk.query_daily_adjust_factor(date=p["date"])
    return sdk.query_dividend_data(p["code"], year=p["year"], yearType=p["year_type"])


def normalize(operation: str, fields: list[str], rows: list[list[str]]):
    records, issues = [], []
    missing = sorted(set(FIELDS[operation]) - set(fields))
    if missing:
        return [], [{"code": "FIELD_CONTRACT", "fields": missing}]
    for index, row in enumerate(rows):
        try:
            if len(row) != len(fields) or any(not isinstance(v, str) for v in row):
                raise ValueError("invalid source row width/type")
            raw = dict(zip(fields, row, strict=True))
            if operation in ("history", "daily_history"):
                if raw["adjustflag"] == "2":
                    # Verification samples retain supplier OHLC decimal strings.
                    record = {"kind": "qfq_sample", "source_record": raw}
                else:
                    record = normalize_daily_record(raw)
                    record.setdefault("kind", "daily")
            elif operation in ("stock_basic", "trade_dates", "all_stock"):
                record = normalize_reference(operation, raw)
            else:
                record = normalize_action(operation, raw)
            records.append(record)
        except (ValueError, KeyError, TypeError) as error:
            excluded = str(error).startswith("EXCLUDED_SECURITY")
            issues.append(
                {
                    "code": "EXCLUDED_SECURITY" if excluded else "INVALID_RECORD",
                    "row": index,
                    "message": str(error),
                }
            )
    return records, issues


def response(sdk: Any, query: dict[str, Any]) -> dict[str, Any]:
    operation, params = query["operation"], query["params"]
    try:
        fields, rows, code, message, complete = consume(invoke(sdk, operation, params))
    except AttributeError:
        fields, rows, code, message, complete = [], [], "UNSUPPORTED", "SDK export missing", False
    records, issues = normalize(operation, fields, rows) if code == "0" else ([], [])
    return {
        "kind": "source_response",
        "query_id": query["query_id"],
        "operation": operation,
        "request": params,
        "fields": fields,
        "rows": rows,
        "fetched_at": datetime.now(timezone.utc).isoformat(),
        "provider_published_at": None,
        "error_code": code,
        "error_message": message,
        "complete": complete,
        "records": records,
        "issues": issues,
    }
