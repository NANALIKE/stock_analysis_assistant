package securities

import "context"

type Instrument struct {
	ID         string  `json:"id"`
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Exchange   string  `json:"exchange"`
	ListedOn   string  `json:"listed_on"`
	DelistedOn *string `json:"delisted_on"`
	Status     string  `json:"status"`
}

type Repository interface {
	Search(context.Context, string) ([]Instrument, error)
	Get(context.Context, string) (Instrument, error)
}
