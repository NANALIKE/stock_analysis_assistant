import json
import unittest
from pathlib import Path

from stock_worker.providers.daily_model import DAILY_FIELDS, normalize_daily_record


class DailyModelTests(unittest.TestCase):
    def setUp(self):
        fixture = Path(__file__).resolve().parents[2] / "testdata" / "baostock-daily.json"
        self.raw = json.loads(fixture.read_text())["record"]

    def test_all_documented_fields_and_unknown_fields_are_preserved(self):
        raw = self.raw | {"future_source_field": "unchanged"}
        result = normalize_daily_record(raw)
        self.assertEqual(set(self.raw), set(DAILY_FIELDS))
        self.assertEqual(result["source_record"], raw)
        self.assertEqual(result["bar"]["amount"], 479255191900)
        self.assertEqual(result["bar"]["previous_close"], 106800)
        self.assertEqual(result["bar"]["turnover_rate"], 0.003925)
        self.assertEqual(result["bar"]["change_rate"], 0.005618)
        self.assertEqual(result["valuation"]["pcf_ncf_ttm"], -58.658491)

    def test_missing_optional_values_are_not_zero(self):
        raw = self.raw | {
            "turn": "",
            "pctChg": "",
            "peTTM": "",
            "isST": "",
            "preclose": "",
        }
        result = normalize_daily_record(raw)
        self.assertIsNone(result["bar"]["turnover_rate"])
        self.assertIsNone(result["bar"]["change_rate"])
        self.assertIsNone(result["bar"]["previous_close"])
        self.assertIsNone(result["bar"]["is_st"])
        self.assertIsNone(result["valuation"]["pe_ttm"])

    def test_four_decimal_amount_and_exchange_previous_close(self):
        # Boundary inputs intentionally change a fixture; these are numeric tests,
        # not claimed market observations.
        raw = self.raw | {"amount": "1.2345", "preclose": "9.9999"}
        result = normalize_daily_record(raw)
        self.assertEqual(result["bar"]["amount"], 12345)
        self.assertEqual(result["bar"]["previous_close"], 99999)
        self.assertEqual(result["source_record"]["preclose"], "9.9999")

    def test_invalid_mandatory_values_are_rejected(self):
        for patch in (
            {"close": ""},
            {"amount": "1.00001"},
            {"amount": "9223372036854775808"},
            {"volume": "1.5"},
            {"open": "NaN"},
            {"adjustflag": "2"},
            {"tradestatus": "2"},
            {"peTTM": "NaN"},
        ):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                normalize_daily_record(self.raw | patch)
