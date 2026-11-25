package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const PlanVersion = "baostock-collection-v1"

// Query keys identify immutable source requests, independently of attempts.
type Query struct {
	ID        string            `json:"query_id"`
	Operation string            `json:"operation"`
	Params    map[string]string `json:"params"`
}

func NewQuery(operation string, params map[string]string) Query {
	body, _ := json.Marshal(struct {
		Operation string            `json:"operation"`
		Params    map[string]string `json:"params"`
	}{operation, params})
	sum := sha256.Sum256(body)
	return Query{ID: hex.EncodeToString(sum[:]), Operation: operation, Params: params}
}

type CollectionContext struct {
	JobID          string   `json:"job_id"`
	ParentVersion  string   `json:"parent_version"`
	AsOf           string   `json:"as_of"`
	From           string   `json:"from"`
	To             string   `json:"to"`
	SelectionScope string   `json:"selection_scope"`
	Symbols        []string `json:"symbols"`
	PlanVersion    string   `json:"plan_version"`
}

type Executor interface {
	Execute(context.Context, Job) error
}

type Retrier interface {
	Retry(context.Context, string) (Job, error)
}
