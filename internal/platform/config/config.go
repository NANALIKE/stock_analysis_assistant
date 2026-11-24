package config

import (
	"fmt"
	"net"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Address string `yaml:"address"`
	} `yaml:"server"`
	Storage struct {
		Database     string `yaml:"database"`
		RawDir       string `yaml:"raw_dir"`
		ArtifactsDir string `yaml:"artifacts_dir"`
		BackupDir    string `yaml:"backup_dir"`
	} `yaml:"storage"`
	Worker struct {
		Python         string        `yaml:"python"`
		Directory      string        `yaml:"directory"`
		Timeout        time.Duration `yaml:"timeout"`
		MaxOutputBytes int64         `yaml:"max_output_bytes"`
	} `yaml:"worker"`
	MarketData struct {
		Provider        string  `yaml:"provider"`
		MinimumCoverage float64 `yaml:"minimum_coverage"`
	} `yaml:"market_data"`
}

func Load(path string) (Config, error) {
	var c Config
	file, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil {
		return c, err
	}
	for key, target := range map[string]*string{
		"STOCK_SERVER_ADDRESS": &c.Server.Address,
		"STOCK_DATABASE":       &c.Storage.Database, "STOCK_RAW_DIR": &c.Storage.RawDir,
		"STOCK_ARTIFACTS_DIR": &c.Storage.ArtifactsDir, "STOCK_BACKUP_DIR": &c.Storage.BackupDir,
		"STOCK_WORKER_PYTHON": &c.Worker.Python, "STOCK_WORKER_DIRECTORY": &c.Worker.Directory,
		"STOCK_PROVIDER": &c.MarketData.Provider,
	} {
		if value, ok := os.LookupEnv(key); ok {
			*target = value
		}
	}
	host, _, err := net.SplitHostPort(c.Server.Address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return c, fmt.Errorf("server.address must use a loopback IP and port")
	}
	if c.Storage.Database == "" || c.Storage.RawDir == "" || c.Storage.ArtifactsDir == "" || c.Storage.BackupDir == "" {
		return c, fmt.Errorf("storage paths must not be empty")
	}
	if c.Worker.Timeout <= 0 || c.Worker.MaxOutputBytes <= 0 || c.Worker.Python == "" || c.Worker.Directory == "" {
		return c, fmt.Errorf("worker configuration is invalid")
	}
	if c.MarketData.Provider != "baostock" || c.MarketData.MinimumCoverage < 0.995 || c.MarketData.MinimumCoverage > 1 {
		return c, fmt.Errorf("market_data requires baostock and minimum_coverage in [0.995, 1]")
	}
	return c, nil
}
