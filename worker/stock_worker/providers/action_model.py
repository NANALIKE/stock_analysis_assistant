"""Preserve decimal factors and the complete dividend record."""

import hashlib
import json
from typing import Any

from .daily_model import _decimal
from .reference_model import iso, symbol

DIVIDEND_MAP = {
    "dividPreNoticeDate": "pre_notice_date",
    "dividAgmPumDate": "agm_announce_date",
    "dividPlanAnnounceDate": "plan_announce_date",
    "dividPlanDate": "implementation_announce_date",
    "dividRegistDate": "record_date",
    "dividOperateDate": "effective_date",
    "dividPayDate": "pay_date",
    "dividStockMarketDate": "stock_listing_date",
    "dividCashPsBeforeTax": "cash_per_share_before_tax",
    "dividCashPsAfterTax": "cash_per_share_after_tax_raw",
    "dividStocksPs": "stock_dividend_per_share",
    "dividReserveToStockPs": "reserve_transfer_per_share",
    "dividCashStock": "description",
}
DIVIDEND_FIELDS = ("code", *DIVIDEND_MAP)


def normalize_action(operation: str, raw: dict[str, str]) -> dict[str, Any]:
    identity = symbol(raw["code"])
    if operation in ("adjust_factor", "daily_adjust_factor"):
        result = {
            "kind": "factor",
            "symbol": identity,
            "effective_date": iso(raw["dividOperateDate"]),
            "source_record": raw,
        }
        for source, target in (
            ("foreAdjustFactor", "fore_adjust_factor"),
            ("backAdjustFactor", "back_adjust_factor"),
            ("adjustFactor", "adjust_factor"),
        ):
            if _decimal(raw[source]) <= 0:
                raise ValueError("factor must be positive")
            result[target] = raw[source]
        return result
    result = {"kind": "dividend", "symbol": identity, "source_record": raw}
    for source, target in DIVIDEND_MAP.items():
        value = raw[source]
        if target.endswith("_date"):
            result[target] = iso(value, nullable=True)
        else:
            if target not in ("cash_per_share_after_tax_raw", "description") and value:
                _decimal(value)
            result[target] = value or None
    # The same response row returned via report/operate is the same observation.
    content = json.dumps(raw, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    result["record_sha256"] = hashlib.sha256(content.encode()).hexdigest()
    return result
