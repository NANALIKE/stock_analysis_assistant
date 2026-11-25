import unittest
from unittest.mock import patch

from stock_worker.protocol import Request
from stock_worker.providers.baostock import collect
from stock_worker.providers.daily_model import DAILY_FIELDS
from stock_worker.providers.response import normalize, response, validate_queries


class ResponseTests(unittest.TestCase):
    def test_replay_original_strings_without_sdk(self):
        query = {
            "query_id": "replay",
            "operation": "trade_dates",
            "params": {"start_date": "2020-01-02", "end_date": "2020-01-02"},
        }
        raw = {
            "kind": "source_response",
            "query_id": "replay",
            "operation": query["operation"],
            "request": query["params"],
            "fields": ["calendar_date", "is_trading_day", "unknown"],
            "rows": [["2020-01-02", "1", "retained"]],
            "error_code": "0",
            "complete": True,
            "records": [{"stale_algorithm": True}],
        }
        request = Request.parse(
            {
                "schema_version": 1,
                "run_id": "offline",
                "provider": "baostock",
                "operation": "collect",
                "as_of": "2020-01-02",
                "params": {"queries": [query], "replay": raw},
            }
        )
        with patch("stock_worker.providers.baostock.session", side_effect=AssertionError("SDK")):
            saved = list(collect(request))[0]
        self.assertEqual(saved["rows"], raw["rows"])
        self.assertTrue(saved["records"][0]["is_open"])
        self.assertEqual(saved["records"][0]["source_record"]["unknown"], "retained")

    def test_contract_invalid_row_and_unknown_fields(self):
        records, issues = normalize("history", ["date"], [["2020-01-02"]])
        self.assertEqual(records, [])
        self.assertEqual(issues[0]["code"], "FIELD_CONTRACT")
        records, issues = normalize(
            "trade_dates", ["calendar_date", "is_trading_day"], [["bad", "1"]]
        )
        self.assertEqual(records, [])
        self.assertEqual(issues[0]["code"], "INVALID_RECORD")
        self.assertEqual(len(DAILY_FIELDS), 18)

    def test_missing_sdk_export_and_parameter_whitelist(self):
        query = {
            "query_id": "sample",
            "operation": "daily_history",
            "params": {"date": "2020-01-02"},
        }
        result = response(object(), query)
        self.assertEqual(result["error_code"], "UNSUPPORTED")
        self.assertFalse(result["complete"])
        self.assertEqual(validate_queries({"queries": [query]}), [query])
        with self.assertRaises(ValueError):
            validate_queries({"queries": [query | {"operation": "__dict__"}]})
