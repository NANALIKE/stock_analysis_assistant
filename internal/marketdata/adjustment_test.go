package marketdata

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
)

func TestAdjustmentAnchorsAndRounding(t *testing.T) {
	factors := []Factor{{Date: "2020-01-03", Fore: "0.5000000000", Back: "2.0000000000", Value: "2.0000000000"},
		{Date: "2021-01-03", Fore: "0.2500000000", Back: "4.0000000000", Value: "2.0000000000"}}
	if err := ValidateFactors(factors); err != nil {
		t.Fatal(err)
	}
	previous := int64(123)
	turnover := 0.3
	bar := FixedBar{TradeDate: "2020-01-02", Open: 100001, High: 120001, Low: 80001, Close: 100001, PreviousClose: &previous, Volume: 500, Amount: 1200, TurnoverRate: &turnover}
	for _, tc := range []struct {
		anchor string
		close  int64
	}{{"2020-01-02", 100001}, {"2020-01-04", 50001}, {"2021-01-04", 25000}} {
		got, err := Adjust(bar, factors, tc.anchor)
		if err != nil {
			t.Fatal(err)
		}
		if got.Close != tc.close || got.PreviousClose != bar.PreviousClose || got.Volume != 500 || got.Amount != 1200 || got.TurnoverRate != bar.TurnoverRate {
			t.Fatalf("anchor=%s got=%+v", tc.anchor, got)
		}
	}
	if got, err := RoundPrice(big.NewRat(1, 2)); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	if _, err := RoundPrice(new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 64))); err == nil {
		t.Fatal("overflow accepted")
	}
	for _, value := range []string{"", "0", "-1", "1/2", "1e2"} {
		if _, err := Decimal(value); err == nil {
			t.Fatalf("accepted factor %q", value)
		}
	}
	if _, err := Adjust(bar, nil, "2021-01-04"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFactors([]Factor{factors[0], factors[0]}); err == nil {
		t.Fatal("duplicate factor accepted")
	}
}

func TestSupplierAnchorAndPrecision(t *testing.T) {
	factors := []Factor{{Date: "2020-01-03", Fore: "0.5000000000", Back: "2.0000000000", Value: "2.0000000000"}}
	bar := FixedBar{TradeDate: "2020-01-02", Open: 100000, High: 100000, Low: 100000, Close: 100000}
	ref := FixedBar{TradeDate: "2020-01-03", Open: 50000, High: 50000, Low: 50000, Close: 50000}
	supplier := map[string]string{"open": "2.5000", "high": "2.5000", "low": "2.5000", "close": "2.5000"}
	checks, err := VerifySample(bar, ref, supplier, supplier, factors, "2020-01-02")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checks {
		if !d.Passed || d.Local != 100000 {
			t.Fatalf("historical anchor: %+v", d)
		}
	}
	supplier["open"] = "2.6000"
	checks, err = VerifySample(bar, ref, supplier, map[string]string{"close": "2.5000"}, factors, "2020-01-03")
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Passed {
		t.Fatal("material mismatch accepted")
	}
}

func TestExchangeReferenceAtFactorStep(t *testing.T) {
	reference := int64(50000)
	factors := []Factor{{Date: "2020-01-03", Fore: "0.5000000000", Back: "2.0000000000", Value: "2.0000000000"}}
	d, err := VerifyExchangeReference(FixedBar{TradeDate: "2020-01-02", Close: 100000}, FixedBar{TradeDate: "2020-01-03", PreviousClose: &reference}, factors)
	if err != nil || !d.Passed {
		t.Fatal(d, err)
	}
	reference = 51000
	d, err = VerifyExchangeReference(FixedBar{TradeDate: "2020-01-02", Close: 100000}, FixedBar{TradeDate: "2020-01-03", PreviousClose: &reference}, factors)
	if err != nil || d.Passed {
		t.Fatal(d, err)
	}
}

func TestRealSupplierEventPrecision(t *testing.T) {
	body, err := os.ReadFile("../../testdata/baostock-event-online.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Responses []SourceResponse }
	if err = json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	factors := []Factor{}
	bars := map[string]FixedBar{}
	samples := map[string]map[string]string{}
	for _, response := range fixture.Responses {
		for _, raw := range response.Records {
			var f Fact
			if err = json.Unmarshal(raw, &f); err != nil {
				t.Fatal(err)
			}
			if f.Kind == "factor" {
				factors = append(factors, Factor{Date: f.EffectiveDate, Fore: f.Fore, Back: f.Back, Value: f.Factor})
			}
			if f.Kind == "daily" {
				bars[f.Bar.TradeDate] = f.Bar
			}
		}
		if response.Request["adjustflag"] == "2" {
			for _, row := range response.Rows {
				sample := map[string]string{}
				for i, field := range response.Fields {
					sample[field] = row[i]
				}
				samples[sample["date"]] = sample
			}
		}
	}
	if len(factors) != 2 || len(samples) != 11 || len(bars) != 11 {
		t.Fatal("incomplete real event fixture")
	}
	for day, bar := range bars {
		checks, err := VerifySample(bar, bars["2000-07-13"], samples[day], samples["2000-07-13"], factors, "2001-08-30")
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range checks {
			if !d.Passed {
				t.Fatalf("real supplier factor precision: %+v", d)
			}
		}
	}
	// A changed supplier price must remain outside the precision-derived bounds.
	samples["2000-06-29"]["high"] = "1.8000000000"
	checks, err := VerifySample(bars["2000-06-29"], bars["2000-07-13"], samples["2000-06-29"], samples["2000-07-13"], factors, "2001-08-30")
	if err != nil || checks[1].Passed {
		t.Fatal("material mismatch accepted", checks, err)
	}
}
