package marketdata

type CoverageCount struct {
	Period   string  `json:"period"`
	Expected int64   `json:"expected"`
	Covered  int64   `json:"covered"`
	Bars     int64   `json:"valid_bars"`
	Coverage float64 `json:"coverage"`
}

type QualityReport struct {
	DatasetVersion      string                 `json:"dataset_version"`
	AsOf                string                 `json:"as_of"`
	SelectionScope      string                 `json:"selection_scope"`
	MarketScope         string                 `json:"market_scope"`
	SourceGaps          []string               `json:"source_gaps"`
	Expected            int64                  `json:"expected_records"`
	Covered             int64                  `json:"covered_records"`
	ValidBars           int64                  `json:"valid_bars"`
	Coverage            float64                `json:"coverage"`
	Daily               []CoverageCount        `json:"daily"`
	Yearly              []CoverageCount        `json:"yearly"`
	ValuationNonNull    map[string]float64     `json:"valuation_non_null_rates"`
	AdjustmentChecks    []AdjustmentDifference `json:"adjustment_checks"`
	AdjustmentArtifacts []string               `json:"adjustment_artifact_ids"`
	CheckedValues       int64                  `json:"checked_adjustment_values"`
	Failures            []string               `json:"failures"`
	Passed              bool                   `json:"passed"`
	Algorithm           string                 `json:"algorithm"`
	ManifestHash        string                 `json:"manifest_sha256"`
}

func (r *QualityReport) Evaluate(minimum float64) {
	if minimum < 0.995 {
		minimum = 0.995
	}
	years := map[string]*CoverageCount{}
	r.Expected, r.Covered, r.ValidBars = 0, 0, 0
	for index := range r.Daily {
		day := &r.Daily[index]
		if day.Expected > 0 {
			day.Coverage = float64(day.Covered) / float64(day.Expected)
		}
		r.Expected += day.Expected
		r.Covered += day.Covered
		r.ValidBars += day.Bars
		if day.Expected > 0 && day.Coverage < minimum {
			r.Failures = append(r.Failures, "daily coverage below threshold: "+day.Period)
		}
		year := day.Period[:4]
		if years[year] == nil {
			years[year] = &CoverageCount{Period: year}
			r.Yearly = append(r.Yearly, CoverageCount{Period: year})
		}
		years[year].Expected += day.Expected
		years[year].Covered += day.Covered
		years[year].Bars += day.Bars
	}
	for i := range r.Yearly {
		year := *years[r.Yearly[i].Period]
		if year.Expected > 0 {
			year.Coverage = float64(year.Covered) / float64(year.Expected)
		}
		r.Yearly[i] = year
	}
	if r.Expected > 0 {
		r.Coverage = float64(r.Covered) / float64(r.Expected)
	} else {
		r.Failures = append(r.Failures, "no expected securities days")
	}
	if r.Coverage < minimum {
		r.Failures = append(r.Failures, "overall coverage below threshold")
	}
	for _, d := range r.AdjustmentChecks {
		if !d.Passed {
			r.Failures = append(r.Failures, "qfq mismatch: "+d.Date+" "+d.Field)
		}
	}
	r.Passed = len(r.Failures) == 0
}
