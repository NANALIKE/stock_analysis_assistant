package delivery

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stock_analysis_assistant/internal/jobs"
	"stock_analysis_assistant/internal/marketdata"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/securities"
)

type API struct {
	Jobs       jobs.Repository
	MarketData marketdata.Repository
	Securities securities.Repository
	Provider   string
	Logger     *slog.Logger
}

func (api API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/market-data/generations", api.generate)
	mux.HandleFunc("GET /api/v1/market-data/generations/{id}", api.generation)
	mux.HandleFunc("GET /api/v1/market-data/status", api.status)
	mux.HandleFunc("GET /api/v1/instruments", api.instruments)
	mux.HandleFunc("GET /api/v1/instruments/{id}/daily-bars", api.bars)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w.Header().Set("X-Request-ID", platform.NewID("req"))
		mux.ServeHTTP(w, r)
		api.Logger.Info("http request", "request_id", w.Header().Get("X-Request-ID"), "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (api API) fail(w http.ResponseWriter, err error) {
	failure := &platform.Error{Code: "INTERNAL_ERROR", Message: "internal error"}
	status := http.StatusInternalServerError
	if errors.As(err, &failure) {
		switch failure.Code {
		case "INVALID_ARGUMENT":
			status = http.StatusBadRequest
		case "GENERATION_NOT_FOUND", "INSTRUMENT_NOT_FOUND":
			status = http.StatusNotFound
		case "NO_ACTIVE_DATASET", "SOURCE_UNAVAILABLE":
			status = http.StatusServiceUnavailable
		}
	} else {
		api.Logger.Error("http failure", "request_id", w.Header().Get("X-Request-ID"), "error", err)
	}
	respond(w, status, struct {
		Error     *platform.Error `json:"error"`
		RequestID string          `json:"request_id"`
	}{failure, w.Header().Get("X-Request-ID")})
}

func (api API) generate(w http.ResponseWriter, r *http.Request) {
	retry := r.URL.Query().Get("retry")
	if retry != "" && retry != "true" && retry != "false" {
		api.fail(w, &platform.Error{Code: "INVALID_ARGUMENT", Message: "retry must be true or false"})
		return
	}
	var request jobs.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		api.fail(w, &platform.Error{Code: "INVALID_ARGUMENT", Message: "invalid generation JSON"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		api.fail(w, &platform.Error{Code: "INVALID_ARGUMENT", Message: "expected one JSON object"})
		return
	}
	job, err := api.Jobs.Create(r.Context(), request, api.Provider)
	if err != nil {
		api.fail(w, err)
		return
	}
	if retry == "true" && job.Status == "failed" {
		repository, ok := api.Jobs.(jobs.Retrier)
		if !ok {
			api.fail(w, &platform.Error{Code: "INTERNAL_ERROR", Message: "retry is unavailable"})
			return
		}
		job, err = repository.Retry(r.Context(), job.ID)
		if err != nil {
			api.fail(w, err)
			return
		}
	}
	url := "/api/v1/market-data/generations/" + job.ID
	w.Header().Set("Location", url)
	respond(w, http.StatusAccepted, map[string]string{"generation_id": job.ID, "status": job.Status, "status_url": url})
}

func (api API) generation(w http.ResponseWriter, r *http.Request) {
	job, err := api.Jobs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		api.fail(w, err)
		return
	}
	respond(w, http.StatusOK, job)
}

func (api API) status(w http.ResponseWriter, r *http.Request) {
	dataset, err := api.MarketData.Active(r.Context())
	if err != nil {
		api.fail(w, err)
		return
	}
	last, err := api.Jobs.Latest(r.Context())
	if err != nil {
		api.fail(w, err)
		return
	}
	respond(w, http.StatusOK, struct {
		marketdata.Dataset
		Stale   bool      `json:"stale"`
		LastJob *jobs.Job `json:"last_job"`
	}{dataset, dataset.Stale, last})
}

func (api API) instruments(w http.ResponseWriter, r *http.Request) {
	result, err := api.Securities.Search(r.Context(), strings.TrimSpace(r.URL.Query().Get("query")))
	if err != nil {
		api.fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"instruments": result})
}

func (api API) bars(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	adjustment := query.Get("adjustment")
	if adjustment == "" {
		adjustment = "qfq"
	}
	if err := jobs.ValidateRange(query.Get("from"), query.Get("to")); err != nil {
		api.fail(w, &platform.Error{Code: "INVALID_ARGUMENT", Message: err.Error()})
		return
	}
	if adjustment != "qfq" && adjustment != "none" {
		api.fail(w, &platform.Error{Code: "INVALID_ARGUMENT", Message: "adjustment must be qfq or none"})
		return
	}
	result, err := api.MarketData.Bars(r.Context(), r.PathValue("id"), query.Get("from"), query.Get("to"), adjustment)
	if err != nil {
		api.fail(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}
