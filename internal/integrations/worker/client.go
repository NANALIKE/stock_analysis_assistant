package worker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type Client struct {
	Python         string
	Directory      string
	Timeout        time.Duration
	MaxOutputBytes int64
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if int64(b.buffer.Len())+int64(len(p)) > b.limit {
		return 0, fmt.Errorf("worker output exceeds limit")
	}
	return b.buffer.Write(p)
}

func (c Client) Execute(ctx context.Context, request Request) (Batch, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	body, err := json.Marshal(request)
	if err != nil {
		return Batch{}, err
	}
	command := exec.CommandContext(ctx, c.Python, "-m", "stock_worker")
	command.WaitDelay = 5 * time.Second
	command.Dir = c.Directory
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	command.Stdin = bytes.NewReader(body)
	stdout := &boundedBuffer{limit: c.MaxOutputBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		// Supplier stderr is deliberately kept out of API-facing errors.
		return Batch{}, fmt.Errorf("worker failed: %w", err)
	}
	return Decode(bytes.NewReader(stdout.buffer.Bytes()), request.RunID, c.MaxOutputBytes)
}

func Decode(input io.Reader, runID string, maxBytes int64) (Batch, error) {
	var batch Batch
	if maxBytes <= 0 {
		return batch, fmt.Errorf("invalid output limit")
	}
	body, err := io.ReadAll(io.LimitReader(input, maxBytes+1))
	if err != nil || int64(len(body)) > maxBytes {
		return batch, fmt.Errorf("worker output exceeds limit or cannot be read")
	}
	reader := bufio.NewReader(bytes.NewReader(body))
	hash := sha256.New()
	ended := false
	started := false
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return Batch{}, fmt.Errorf("truncated worker line")
		}
		var envelope struct {
			Type        string          `json:"type"`
			Data        json.RawMessage `json:"data"`
			RecordCount int             `json:"record_count"`
			SHA256      string          `json:"sha256"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil || ended {
			return Batch{}, fmt.Errorf("invalid worker envelope")
		}
		if !started {
			if err := json.Unmarshal(line, &batch.Manifest); err != nil {
				return Batch{}, err
			}
			m := batch.Manifest
			lockHash, hashErr := hex.DecodeString(m.DependencyLockSHA256)
			if m.Type != "begin" || m.SchemaVersion != SchemaVersion || m.RunID != runID ||
				m.WorkerVersion == "" || m.AdapterVersion == "" || hashErr != nil || len(lockHash) != sha256.Size {
				return Batch{}, fmt.Errorf("invalid worker manifest")
			}
			started = true
			batch.Records = make([]json.RawMessage, 0)
			continue
		}
		switch envelope.Type {
		case "record":
			data := bytes.TrimSpace(envelope.Data)
			if len(data) == 0 || data[0] != '{' {
				return Batch{}, fmt.Errorf("record data must be an object")
			}
			hash.Write(line)
			batch.Records = append(batch.Records, envelope.Data)
		case "end":
			sum := hex.EncodeToString(hash.Sum(nil))
			if envelope.RecordCount != len(batch.Records) || envelope.SHA256 != sum {
				return Batch{}, fmt.Errorf("worker count or hash mismatch")
			}
			batch.SHA256, ended = sum, true
		default:
			return Batch{}, fmt.Errorf("unexpected worker envelope")
		}
	}
	if !ended {
		return Batch{}, fmt.Errorf("missing worker end summary")
	}
	return batch, nil
}
