package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"stock_analysis_assistant/internal/integrations/persistence"
	"stock_analysis_assistant/internal/platform"
	"stock_analysis_assistant/internal/platform/config"
	"stock_analysis_assistant/internal/platform/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: stockctl <init|generate|doctor|backup> [flags]")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "configs/config.example.yaml", "YAML configuration")
	requestPath := flags.String("request", "", "generation request JSON file (generate)")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	c, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if command == "generate" {
		if *requestPath == "" {
			return fmt.Errorf("generate requires -request <file.json>")
		}
		body, err := os.ReadFile(*requestPath)
		if err != nil {
			return err
		}
		// Go remains the writer: the CLI submits to stockd's command endpoint.
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.Server.Address+"/api/v1/market-data/generations", bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return fmt.Errorf("stockd must be running for generate: %w", err)
		}
		defer response.Body.Close()
		if _, err := io.Copy(os.Stdout, response.Body); err != nil {
			return err
		}
		if response.StatusCode != http.StatusAccepted {
			return fmt.Errorf("generation returned HTTP %d", response.StatusCode)
		}
		return nil
	}
	if command != "init" && command != "doctor" && command != "backup" {
		return fmt.Errorf("unknown command %q", command)
	}
	// Doctor and backup must never create a new empty database over a mistyped path.
	if command != "init" {
		if _, err := os.Stat(c.Storage.Database); err != nil {
			return err
		}
	}
	db, err := database.Open(ctx, c.Storage.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	switch command {
	case "init":
		if err := database.Migrate(ctx, db); err != nil {
			return err
		}
		for _, path := range []string{c.Storage.RawDir, c.Storage.ArtifactsDir, c.Storage.BackupDir} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "initialized", "database": c.Storage.Database})
	case "doctor":
		var integrity, journal string
		var foreignKeys int
		if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			return err
		}
		latest, err := (persistence.Jobs{DB: db}).Latest(ctx)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"integrity": integrity, "journal_mode": journal, "foreign_keys": foreignKeys, "last_job": latest}); err != nil {
			return err
		}
		if integrity != "ok" {
			return fmt.Errorf("database integrity check failed")
		}
	case "backup":
		root := filepath.Join(c.Storage.BackupDir, platform.NewID("backup"))
		if err := os.MkdirAll(root, 0o700); err != nil {
			return err
		}
		complete := false
		defer func() {
			if !complete {
				os.RemoveAll(root)
			}
		}()
		if err := database.Backup(ctx, db, filepath.Join(root, "stock.db")); err != nil {
			return err
		}
		for name, source := range map[string]string{"raw": c.Storage.RawDir, "artifacts": c.Storage.ArtifactsDir} {
			if err := copyTree(source, filepath.Join(root, name)); err != nil {
				return err
			}
		}
		manifest := map[string]string{}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(body)
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			manifest[name] = hex.EncodeToString(hash[:])
			return nil
		}); err != nil {
			return err
		}
		body, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "manifest.json"), body, 0o600); err != nil {
			return err
		}
		complete = true
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"backup": root})
	}
	return nil
}

func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("backup only supports regular artifacts: %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
