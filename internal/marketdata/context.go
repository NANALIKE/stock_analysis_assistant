package marketdata

import (
	"context"
	"encoding/json"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/securities"
)

type SourceResponse struct {
	Kind                string            `json:"kind"`
	QueryID             string            `json:"query_id"`
	Operation           string            `json:"operation"`
	Request             map[string]string `json:"request"`
	Fields              []string          `json:"fields"`
	Rows                [][]string        `json:"rows"`
	FetchedAt           string            `json:"fetched_at"`
	ProviderPublishedAt *string           `json:"provider_published_at"`
	ErrorCode           string            `json:"error_code"`
	ErrorMessage        string            `json:"error_message"`
	Complete            bool              `json:"complete"`
	Records             []json.RawMessage `json:"records"`
	Issues              []Issue           `json:"issues"`
}

type Issue struct {
	Code    string   `json:"code"`
	Row     *int     `json:"row,omitempty"`
	Message string   `json:"message,omitempty"`
	Fields  []string `json:"fields,omitempty"`
}

func (r SourceResponse) Matches(q jobs.Query) bool {
	actual := jobs.NewQuery(r.Operation, r.Request)
	return r.Kind == "source_response" && r.QueryID == q.ID && actual.ID == q.ID
}

type FixedBar struct {
	TradeDate     string   `json:"trade_date"`
	Adjustment    string   `json:"adjustment"`
	Open          int64    `json:"open"`
	High          int64    `json:"high"`
	Low           int64    `json:"low"`
	Close         int64    `json:"close"`
	PreviousClose *int64   `json:"previous_close"`
	Volume        int64    `json:"volume"`
	Amount        int64    `json:"amount"`
	TurnoverRate  *float64 `json:"turnover_rate"`
	ChangeRate    *float64 `json:"change_rate"`
	TradeStatus   string   `json:"trade_status"`
	IsST          *bool    `json:"is_st"`
}

type Fact struct {
	Kind          string            `json:"kind"`
	Symbol        string            `json:"symbol"`
	Name          string            `json:"name"`
	Exchange      string            `json:"exchange"`
	ListedOn      string            `json:"listed_on"`
	DelistedOn    *string           `json:"delisted_on"`
	Status        string            `json:"status"`
	TradeDate     string            `json:"trade_date"`
	IsOpen        bool              `json:"is_open"`
	TradeStatus   string            `json:"trade_status"`
	IsST          *bool             `json:"is_st"`
	Bar           FixedBar          `json:"bar"`
	Valuation     Valuation         `json:"valuation"`
	EffectiveDate string            `json:"effective_date"`
	Fore          string            `json:"fore_adjust_factor"`
	Back          string            `json:"back_adjust_factor"`
	Factor        string            `json:"adjust_factor"`
	SourceRecord  map[string]string `json:"source_record"`
}

type Range struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type SourceManifest struct {
	WorkerVersion        string
	DependencyLockSHA256 string
	AdapterVersion       string
}

type Source interface {
	Query(context.Context, string, string, jobs.Query, *SourceResponse) (SourceResponse, SourceManifest, error)
}

type BuildStore interface {
	Begin(context.Context, jobs.Job) (jobs.CollectionContext, string, error)
	SetTarget(context.Context, string, string, string) error
	Stage(context.Context, string, string) error
	Unit(context.Context, string, jobs.Query) (string, *SourceResponse, error)
	StartCall(context.Context, jobs.Job, jobs.Query) (string, error)
	CallFailed(context.Context, jobs.Job, jobs.Query, string, error) error
	RecordManifest(context.Context, jobs.Job, SourceManifest) error
	SaveResponse(context.Context, jobs.Job, string, jobs.Query, string, SourceResponse, SourceManifest) (string, error)
	Replay(context.Context, string, string, SourceResponse) error
	Universe(context.Context) ([]securities.Instrument, error)
	ParentSymbols(context.Context, string) ([]string, error)
	AddRanges(context.Context, string, []securities.Instrument, string, string) error
	Missing(context.Context, string, []securities.Instrument, []string) (map[string][]string, error)
	Finish(context.Context, jobs.Job, string, float64) error
}
