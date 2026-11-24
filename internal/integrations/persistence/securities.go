package persistence

import (
	"context"
	"database/sql"
	"errors"

	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/securities"
)

type Securities struct{ DB *sql.DB }

func (r Securities) Search(ctx context.Context, query string) ([]securities.Instrument, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,symbol,name,exchange,listed_on,delisted_on,status FROM instruments
		WHERE instr(symbol,?)>0 OR instr(name,?)>0 OR id IN
		(SELECT instrument_id FROM instrument_aliases WHERE instr(alias,?)>0) ORDER BY symbol LIMIT 100`, query, query, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]securities.Instrument, 0)
	for rows.Next() {
		var instrument securities.Instrument
		if err := rows.Scan(&instrument.ID, &instrument.Symbol, &instrument.Name, &instrument.Exchange, &instrument.ListedOn, &instrument.DelistedOn, &instrument.Status); err != nil {
			return nil, err
		}
		result = append(result, instrument)
	}
	return result, rows.Err()
}

func (r Securities) Get(ctx context.Context, id string) (securities.Instrument, error) {
	var instrument securities.Instrument
	err := r.DB.QueryRowContext(ctx, `SELECT id,symbol,name,exchange,listed_on,delisted_on,status FROM instruments WHERE id=?`, id).
		Scan(&instrument.ID, &instrument.Symbol, &instrument.Name, &instrument.Exchange, &instrument.ListedOn, &instrument.DelistedOn, &instrument.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return instrument, &platform.Error{Code: "INSTRUMENT_NOT_FOUND", Message: "instrument not found"}
	}
	return instrument, err
}
