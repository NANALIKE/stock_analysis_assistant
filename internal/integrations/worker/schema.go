package worker

import "encoding/json"

const SchemaVersion = 1

type Request struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         string         `json:"run_id"`
	Operation     string         `json:"operation"`
	Provider      string         `json:"provider"`
	AsOf          string         `json:"as_of"`
	Params        map[string]any `json:"params"`
}

type Manifest struct {
	Type                 string `json:"type"`
	SchemaVersion        int    `json:"schema_version"`
	RunID                string `json:"run_id"`
	WorkerVersion        string `json:"worker_version"`
	DependencyLockSHA256 string `json:"dependency_lock_sha256"`
	AdapterVersion       string `json:"adapter_version"`
}

type Batch struct {
	Manifest Manifest
	Records  []json.RawMessage
	SHA256   string
}
