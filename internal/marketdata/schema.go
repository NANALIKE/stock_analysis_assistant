package marketdata

import (
	"context"

	"stock_analysis_assistant/internal/securities"
)

type Dataset struct {
	ID                string  `json:"dataset_version"`
	AsOf              string  `json:"as_of"`
	Source            string  `json:"source"`
	Adjustment        string  `json:"adjustment"`
	Coverage          float64 `json:"coverage"`
	Stale             bool    `json:"-"`
	MarketScope       string  `json:"market_scope"`
	SelectionScope    string  `json:"selection_scope"`
	From              string  `json:"from"`
	To                string  `json:"to"`
	UniverseSize      int64   `json:"universe_size"`
	RangesUniform     bool    `json:"ranges_uniform"`
	ExpectedRecords   int64   `json:"expected_records"`
	CoveredRecords    int64   `json:"covered_records"`
	QualityArtifactID *string `json:"quality_artifact_id"`
}

type Bar struct {
	TradeDate     string     `json:"trade_date"`
	Open          float64    `json:"open"`
	High          float64    `json:"high"`
	Low           float64    `json:"low"`
	Close         float64    `json:"close"`
	Volume        int64      `json:"volume"`
	Amount        float64    `json:"amount"`
	IsSuspended   bool       `json:"is_suspended"`
	PreviousClose *float64   `json:"previous_close"`
	TurnoverRate  *float64   `json:"turnover_rate"`
	ChangeRate    *float64   `json:"change_rate"`
	TradeStatus   string     `json:"trade_status"`
	IsST          *bool      `json:"is_st"`
	Valuation     *Valuation `json:"valuation"`
}

type Valuation struct {
	PETTM     *float64 `json:"pe_ttm"`
	PBMRQ     *float64 `json:"pb_mrq"`
	PSTTM     *float64 `json:"ps_ttm"`
	PCFNCFTTM *float64 `json:"pcf_ncf_ttm"`
}

type DateStatus struct {
	TradeDate   string  `json:"trade_date"`
	CodeName    *string `json:"code_name"`
	TradeStatus string  `json:"trade_status"`
	IsST        *bool   `json:"is_st"`
	ReasonCode  *string `json:"reason_code"`
}

type DailyBars struct {
	Instrument securities.Instrument `json:"instrument"`
	Dataset
	Bars           []Bar        `json:"bars"`
	DateStatuses   []DateStatus `json:"date_statuses"`
	CoverageRanges []Range      `json:"coverage_ranges"`
}

type Repository interface {
	Active(context.Context) (Dataset, error)
	Bars(context.Context, string, string, string, string) (DailyBars, error)
}
