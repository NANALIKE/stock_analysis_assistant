package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"stock_analysis_assistant/internal/platform"
)

type Request struct {
	Mode       string   `json:"mode"`
	TradeDate  string   `json:"trade_date,omitempty"`
	From       string   `json:"from,omitempty"`
	To         string   `json:"to,omitempty"`
	Symbols    []string `json:"symbols"`
	Adjustment string   `json:"adjustment"`
}

var symbolPattern = regexp.MustCompile(`^[0-9]{6}\.(SH|SZ|BJ)$`)

func (r *Request) Normalize() error {
	if r.Adjustment == "" {
		r.Adjustment = "qfq"
	}
	if r.Adjustment != "qfq" && r.Adjustment != "none" {
		return fmt.Errorf("adjustment must be qfq or none")
	}
	switch r.Mode {
	case "daily":
		if _, err := time.Parse(time.DateOnly, r.TradeDate); err != nil || r.From != "" || r.To != "" {
			return fmt.Errorf("daily requires trade_date and excludes from/to")
		}
	case "backfill":
		if err := ValidateRange(r.From, r.To); err != nil || r.TradeDate != "" {
			return fmt.Errorf("backfill requires valid from/to and excludes trade_date")
		}
	default:
		return fmt.Errorf("mode must be daily or backfill")
	}
	symbols := make([]string, 0, len(r.Symbols))
	for _, symbol := range r.Symbols {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if !symbolPattern.MatchString(symbol) {
			return fmt.Errorf("symbols must use six digits with .SH, .SZ or .BJ")
		}
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	r.Symbols = make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if len(r.Symbols) == 0 || r.Symbols[len(r.Symbols)-1] != symbol {
			r.Symbols = append(r.Symbols, symbol)
		}
	}
	return nil
}

func ValidateRange(from, to string) error {
	start, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return fmt.Errorf("from must be an ISO date")
	}
	end, err := time.Parse(time.DateOnly, to)
	if err != nil || start.After(end) {
		return fmt.Errorf("to must be an ISO date at or after from")
	}
	return nil
}

func (r Request) Key(provider string) string {
	bytes, _ := json.Marshal(struct {
		Request
		Provider string `json:"provider"`
	}{r, provider})
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}

type Job struct {
	ID                string          `json:"generation_id"`
	Status            string          `json:"status"`
	Stage             string          `json:"stage"`
	Attempt           int             `json:"attempt"`
	Request           Request         `json:"request"`
	ProcessedRecords  int64           `json:"processed_records"`
	ExpectedRecords   int64           `json:"expected_records"`
	CompletedUnits    int64           `json:"completed_units"`
	TotalUnits        int64           `json:"total_units"`
	QualityArtifactID *string         `json:"quality_artifact_id"`
	Coverage          *float64        `json:"coverage"`
	DatasetVersion    *string         `json:"dataset_version"`
	CreatedAt         string          `json:"created_at"`
	StartedAt         *string         `json:"started_at"`
	FinishedAt        *string         `json:"finished_at"`
	Error             *platform.Error `json:"error"`
}

type Repository interface {
	Create(context.Context, Request, string) (Job, error)
	Get(context.Context, string) (Job, error)
	Latest(context.Context) (*Job, error)
	Claim(context.Context) (*Job, error)
	Fail(context.Context, Job, *platform.Error) error
	Recover(context.Context) error
}
