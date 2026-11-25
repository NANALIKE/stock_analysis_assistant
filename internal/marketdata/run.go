package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/securities"
)

type Runner struct {
	Store           BuildStore
	Source          Source
	Interval        time.Duration
	MaxAttempts     int
	MinimumCoverage float64
	Now             func() time.Time
}

type execution struct {
	Runner
	job     jobs.Job
	frozen  jobs.CollectionContext
	version string
	applied map[string]bool
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *execution) query(ctx context.Context, operation string, params map[string]string) (SourceResponse, error) {
	q := jobs.NewQuery(operation, params)
	snapshot, saved, err := e.Store.Unit(ctx, e.job.ID, q)
	if err != nil {
		return SourceResponse{}, err
	}
	if saved != nil {
		if e.applied[q.ID] {
			return *saved, nil
		}
		response, manifest, err := e.Source.Query(ctx, e.job.ID, e.frozen.AsOf, q, saved)
		if err == nil {
			err = e.Store.RecordManifest(ctx, e.job, manifest)
		}
		if err == nil {
			err = e.Store.Replay(ctx, e.version, snapshot, response)
		}
		if err != nil {
			return response, err
		}
		e.applied[q.ID] = true
		return response, nil
	}
	for attempt := 0; attempt < e.MaxAttempts; attempt++ {
		delay := e.Interval
		if attempt == 1 {
			delay = time.Second
		}
		if attempt > 1 {
			delay = 3 * time.Second
		}
		if err = wait(ctx, delay); err != nil {
			return SourceResponse{}, err
		}
		call, callErr := e.Store.StartCall(ctx, e.job, q)
		if callErr != nil {
			return SourceResponse{}, callErr
		}
		response, manifest, failure := e.Source.Query(ctx, e.job.ID, e.frozen.AsOf, q, nil)
		if failure == nil {
			_, failure = e.Store.SaveResponse(ctx, e.job, e.version, q, call, response, manifest)
			if failure == nil && response.Complete && response.ErrorCode == "0" {
				e.applied[q.ID] = true
				return response, nil
			}
			if failure == nil {
				failure = fmt.Errorf("source query %s failed (%s)", operation, response.ErrorCode)
				if response.ErrorCode == "UNSUPPORTED" {
					return response, failure
				}
			}
		} else {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			auditErr := e.Store.CallFailed(auditCtx, e.job, q, call, failure)
			cancel()
			if auditErr != nil {
				return response, auditErr
			}
		}
		// A persistence failure also closes its provider attempt.
		if response.ErrorCode == "0" && failure != nil {
			if err := e.Store.CallFailed(ctx, e.job, q, call, failure); err != nil {
				return response, err
			}
			return response, failure
		}
		err = failure
	}
	return SourceResponse{}, &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: err.Error()}
}

func providerCode(symbol string) string { return strings.ToLower(symbol[7:]) + "." + symbol[:6] }

func facts(response SourceResponse) ([]Fact, error) {
	result := make([]Fact, 0, len(response.Records))
	for _, raw := range response.Records {
		var fact Fact
		if err := json.Unmarshal(raw, &fact); err != nil {
			return nil, err
		}
		result = append(result, fact)
	}
	return result, nil
}

func fieldContract(r SourceResponse) bool {
	for _, issue := range r.Issues {
		if issue.Code == "FIELD_CONTRACT" {
			return true
		}
	}
	return false
}

func (e *execution) history(ctx context.Context, symbol, from, to, adjustment string) error {
	response, err := e.query(ctx, "history", map[string]string{"code": providerCode(symbol), "start_date": from, "end_date": to, "adjustflag": adjustment})
	if err == nil && fieldContract(response) {
		return &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: "history field contract failed"}
	}
	if err == nil {
		return nil
	}
	// Only size/time limits benefit from splitting; SDK/protocol failures do not.
	if from == to || (!strings.Contains(err.Error(), "output exceeds limit") && !strings.Contains(err.Error(), "context deadline exceeded")) {
		return err
	}
	start, _ := time.Parse(time.DateOnly, from)
	end, _ := time.Parse(time.DateOnly, to)
	mid := start.AddDate(0, 0, int(end.Sub(start).Hours()/48))
	if err = e.history(ctx, symbol, from, mid.Format(time.DateOnly), adjustment); err != nil {
		return err
	}
	return e.history(ctx, symbol, mid.AddDate(0, 0, 1).Format(time.DateOnly), to, adjustment)
}

func (r Runner) Execute(ctx context.Context, job jobs.Job) error {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	to := job.Request.To
	if job.Request.Mode == "daily" {
		to = job.Request.TradeDate
	}
	if err := securities.ClosedDate(to, now); err != nil {
		return &platform.Error{Code: "INVALID_ARGUMENT", Message: err.Error()}
	}
	for _, symbol := range job.Request.Symbols {
		if strings.HasSuffix(symbol, ".BJ") {
			return &platform.Error{Code: "INVALID_ARGUMENT", Message: "BaoStock does not cover Beijing A shares"}
		}
	}
	frozen, version, err := r.Store.Begin(ctx, job)
	if err != nil {
		return err
	}
	e := execution{Runner: r, job: job, frozen: frozen, version: version, applied: map[string]bool{}}
	if e.MaxAttempts < 1 {
		e.MaxAttempts = 3
	}
	if _, err = e.query(ctx, "stock_basic", map[string]string{"code": ""}); err != nil {
		return err
	}
	// Calendar includes inherited coverage for whole-version validation and today's stale check.
	calFrom := frozen.From
	if calFrom > "2000-01-01" {
		calFrom = "2000-01-01"
	}
	calTo := now.Format(time.DateOnly)
	if location, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		calTo = now.In(location).Format(time.DateOnly)
	}
	if calTo < frozen.AsOf {
		calTo = frozen.AsOf
	}
	calendar, err := e.query(ctx, "trade_dates", map[string]string{"start_date": calFrom, "end_date": calTo})
	if err != nil {
		return err
	}
	calendarFacts, err := facts(calendar)
	if err != nil {
		return err
	}
	days := []string{}
	seenCalendar := map[string]bool{}
	for _, f := range calendarFacts {
		seenCalendar[f.TradeDate] = true
		if f.IsOpen && f.TradeDate >= frozen.From && f.TradeDate <= frozen.To {
			days = append(days, f.TradeDate)
		}
	}
	start, _ := time.Parse(time.DateOnly, frozen.From)
	end, _ := time.Parse(time.DateOnly, frozen.To)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if !seenCalendar[day.Format(time.DateOnly)] {
			return &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: "source calendar is incomplete"}
		}
	}
	if len(days) == 0 {
		return &platform.Error{Code: "INVALID_ARGUMENT", Message: "request contains no trading days"}
	}
	target := days[len(days)-1]
	if frozen.AsOf > target && frozen.ParentVersion != "" {
		target = frozen.AsOf
	}
	parentSymbols, err := e.Store.ParentSymbols(ctx, frozen.ParentVersion)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, s := range frozen.Symbols {
		selected[s] = true
	}
	if err = e.Store.SetTarget(ctx, job.ID, version, target); err != nil {
		return err
	}
	e.frozen.AsOf = target
	if err = e.Store.Stage(ctx, job.ID, "daily"); err != nil {
		return err
	}
	if frozen.SelectionScope == "all" {
		for _, day := range days {
			response, err := e.query(ctx, "all_stock", map[string]string{"day": day})
			if err != nil {
				return err
			}
			membershipFacts, err := facts(response)
			if err != nil {
				return err
			}
			universe, err := e.Store.Universe(ctx)
			if err != nil {
				return err
			}
			known := map[string]bool{}
			for _, instrument := range universe {
				known[instrument.Symbol] = true
			}
			for _, fact := range membershipFacts {
				if !known[fact.Symbol] {
					discovery, err := e.query(ctx, "stock_basic", map[string]string{"code": providerCode(fact.Symbol)})
					if err != nil {
						return err
					}
					discovered, err := facts(discovery)
					if err != nil {
						return err
					}
					resolved := false
					for _, candidate := range discovered {
						if candidate.Kind == "instrument" && candidate.Symbol == fact.Symbol {
							resolved = true
							break
						}
					}
					if !resolved {
						return &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: "historical A-share is missing from security master: " + fact.Symbol}
					}
					known[fact.Symbol] = true
				}
			}
			q := jobs.NewQuery("all_stock", map[string]string{"day": day})
			snapshot, _, err := e.Store.Unit(ctx, job.ID, q)
			if err != nil {
				return err
			}
			// Reapply membership after historic security discovery.
			if err = e.Store.Replay(ctx, version, snapshot, response); err != nil {
				return err
			}
		}
	}
	universe, err := e.Store.Universe(ctx)
	if err != nil {
		return err
	}
	instruments := []securities.Instrument{}
	found := map[string]bool{}
	for _, instrument := range universe {
		if frozen.SelectionScope == "all" && (instrument.ListedOn > frozen.To || (instrument.DelistedOn != nil && *instrument.DelistedOn < frozen.From)) {
			continue
		}
		if frozen.SelectionScope == "all" || selected[instrument.Symbol] {
			instruments = append(instruments, instrument)
			found[instrument.Symbol] = true
		}
	}
	for s := range selected {
		if !found[s] {
			return &platform.Error{Code: "INVALID_ARGUMENT", Message: "unknown or unsupported A-share symbol " + s}
		}
	}
	if len(instruments) == 0 {
		return &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: "empty A-share universe"}
	}
	fallback := frozen.SelectionScope == "selected"
	if !fallback {
		for _, day := range days {
			response, err := e.query(ctx, "daily_history", map[string]string{"date": day})
			if response.ErrorCode == "UNSUPPORTED" || fieldContract(response) || (err != nil && (strings.Contains(err.Error(), "output exceeds limit") || strings.Contains(err.Error(), "context deadline exceeded"))) {
				fallback = true
				break
			}
			if err != nil {
				return err
			}
			if len(response.Records) == 0 {
				return &platform.Error{Code: "SOURCE_UNAVAILABLE", Message: "empty market history on trading day " + day}
			}
		}
	}
	if fallback {
		for _, instrument := range instruments {
			from := frozen.From
			if instrument.ListedOn > from {
				from = instrument.ListedOn
			}
			to := frozen.To
			if instrument.DelistedOn != nil && *instrument.DelistedOn < to {
				to = *instrument.DelistedOn
			}
			for from <= to {
				yearEnd := from[:4] + "-12-31"
				if yearEnd > to {
					yearEnd = to
				}
				if err = e.history(ctx, instrument.Symbol, from, yearEnd, "3"); err != nil {
					return err
				}
				next, _ := time.Parse(time.DateOnly, yearEnd)
				from = next.AddDate(0, 0, 1).Format(time.DateOnly)
			}
		}
	}
	if err = e.Store.Stage(ctx, job.ID, "repair"); err != nil {
		return err
	}
	missing, err := e.Store.Missing(ctx, version, instruments, days)
	if err != nil {
		return err
	}
	for symbol, missingDays := range missing {
		if len(missingDays) > 0 {
			if err = e.history(ctx, symbol, missingDays[0], missingDays[len(missingDays)-1], "3"); err != nil {
				return err
			}
		}
	}
	if err = e.Store.AddRanges(ctx, version, instruments, frozen.From, frozen.To); err != nil {
		return err
	}
	if err = e.Store.Stage(ctx, job.ID, "factors"); err != nil {
		return err
	}
	// Refresh every logical instrument if advancing a global anchor.
	for _, instrument := range universe {
		if !found[instrument.Symbol] {
			for _, s := range parentSymbols {
				if s == instrument.Symbol {
					instruments = append(instruments, instrument)
					break
				}
			}
		}
	}
	for _, instrument := range instruments {
		if instrument.ListedOn > target {
			continue
		}
		factorResponse, err := e.query(ctx, "adjust_factor", map[string]string{"code": providerCode(instrument.Symbol), "start_date": instrument.ListedOn, "end_date": target})
		if err != nil {
			return err
		}
		ff, err := facts(factorResponse)
		if err != nil {
			return err
		}
		years := map[string]bool{}
		firstYear := instrument.ListedOn[:4]
		if job.Request.Mode == "daily" {
			y, _ := time.Parse(time.DateOnly, target)
			firstYear = y.AddDate(-1, 0, 0).Format("2006")
			if firstYear < instrument.ListedOn[:4] {
				firstYear = instrument.ListedOn[:4]
			}
		}
		for year := firstYear; year <= target[:4]; {
			years[year] = true
			d, _ := time.Parse("2006", year)
			year = d.AddDate(1, 0, 0).Format("2006")
		}
		for _, f := range ff {
			years[f.EffectiveDate[:4]] = true
		}
		for year := range years {
			for _, yearType := range []string{"operate", "report"} {
				if _, err = e.query(ctx, "dividend", map[string]string{"code": providerCode(instrument.Symbol), "year": year, "year_type": yearType}); err != nil {
					return err
				}
			}
		}
		// One bounded sample around every event plus listing and anchor checks.
		windows := []Range{{From: instrument.ListedOn, To: instrument.ListedOn}, {From: target, To: target}}
		for _, f := range ff {
			windows = append(windows, Range{From: f.EffectiveDate, To: f.EffectiveDate})
		}
		for _, window := range windows {
			d, _ := time.Parse(time.DateOnly, window.From)
			from := d.AddDate(0, 0, -7).Format(time.DateOnly)
			to := d.AddDate(0, 0, 7).Format(time.DateOnly)
			if from < instrument.ListedOn {
				from = instrument.ListedOn
			}
			if to > target {
				to = target
			}
			if from > to {
				continue
			}
			for _, flag := range []string{"3", "2"} {
				if err = e.history(ctx, instrument.Symbol, from, to, flag); err != nil {
					return err
				}
			}
		}
	}
	if err = e.Store.Stage(ctx, job.ID, "quality"); err != nil {
		return err
	}
	return e.Store.Finish(ctx, job, version, r.MinimumCoverage)
}
