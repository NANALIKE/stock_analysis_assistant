package worker

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPythonProtocol(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	client := Client{Python: "python3", Directory: "../../../worker", Timeout: 10 * time.Second, MaxOutputBytes: 1 << 20}
	request := Request{SchemaVersion: 1, RunID: "go_contract", Operation: "protocol_check", Provider: "baostock", AsOf: "2026-11-19", Params: map[string]any{}}
	batch, err := client.Execute(context.Background(), request)
	if err != nil || len(batch.Records) != 0 {
		t.Fatalf("protocol check: batch=%+v error=%v", batch, err)
	}
	request.Operation = "collect"
	if _, err := client.Execute(context.Background(), request); err == nil {
		t.Fatal("unadmitted provider must fail")
	}
	request.Operation = "protocol_check"
	client.MaxOutputBytes = 1
	if _, err := client.Execute(context.Background(), request); err == nil {
		t.Fatal("output limit must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Execute(ctx, request); err == nil {
		t.Fatal("canceled worker must fail")
	}
}

func TestDecodeRejectsInvalidBatches(t *testing.T) {
	begin := `{"type":"begin","schema_version":1,"run_id":"test","worker_version":"0.1.0","dependency_lock_sha256":"` + strings.Repeat("0", 64) + `","adapter_version":"v0"}` + "\n"
	end := `{"type":"end","record_count":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}` + "\n"
	for name, body := range map[string]string{
		"missing_summary": begin,
		"truncated":       begin + strings.TrimSuffix(end, "\n"),
		"bad_hash":        begin + strings.Replace(end, "e3b0", "0000", 1),
		"bad_count":       begin + strings.Replace(end, `"record_count":0`, `"record_count":1`, 1),
		"bad_version":     strings.Replace(begin, `"schema_version":1`, `"schema_version":2`, 1) + end,
		"trailing":        begin + end + end,
		"null_record":     begin + `{"type":"record","data":null}` + "\n" + end,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(body), "test", 4096); err == nil {
				t.Fatal("invalid batch accepted")
			}
		})
	}
	if _, err := Decode(strings.NewReader(begin+end), "test", 1); err == nil {
		t.Fatal("oversize batch accepted")
	}
	if _, err := Decode(strings.NewReader(begin+end), "test", 4096); err != nil {
		t.Fatal(err)
	}
}
