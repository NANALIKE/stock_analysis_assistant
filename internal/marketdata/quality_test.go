package marketdata

import "testing"

func TestDailyCoverageCannotHideMissingDay(t *testing.T) {
	report := QualityReport{Daily: []CoverageCount{{Period: "2020-01-02", Expected: 100000, Covered: 100000, Bars: 100000}, {Period: "2020-01-03", Expected: 1, Covered: 0}}}
	report.Evaluate(.995)
	if report.Passed || report.Coverage < .995 || len(report.Yearly) != 1 || report.Expected != 100001 {
		t.Fatalf("%+v", report)
	}
	suspended := QualityReport{Daily: []CoverageCount{{Period: "2020-01-02", Expected: 1, Covered: 1, Bars: 0}}}
	suspended.Evaluate(.995)
	if !suspended.Passed || suspended.ValidBars != 0 {
		t.Fatalf("%+v", suspended)
	}
}
