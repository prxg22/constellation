package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	mux "github.com/prxg22/agents-mux"
)

var (
	cfgFile string
	yesMode bool
	cfg     CLIConfig
)

// CLIConfig holds CLI-level configuration loaded from config.yaml.
type CLIConfig struct {
	DBPath     string `yaml:"db_path"`
	SessionDir string `yaml:"session_dir"`
	HandoffDir string `yaml:"handoff_dir"`
}

func defaultConfig() CLIConfig {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".constellation")
	return CLIConfig{
		DBPath:     filepath.Join(base, "db", "mux.db"),
		SessionDir: filepath.Join(base, "sessions"),
		HandoffDir: filepath.Join(base, "handoffs"),
	}
}

var rootCmd = &cobra.Command{
	Use:   "agents-mux",
	Short: "Constellation AI agent CLI orchestrator",
	Long:  "Constellation manages AI agent sessions, queues, and prompts from the command line. The binary name remains agents-mux for compatibility.",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.constellation/config.yaml, falls back to ~/.agents-mux/config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&yesMode, "yes", "y", false, "non-interactive mode (skip confirmations)")

	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(promptCmd)
	rootCmd.AddCommand(queueCmd)
	rootCmd.AddCommand(agentsCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadConfig() CLIConfig {
	cfg = defaultConfig()
	path := cfgFile
	if path == "" {
		home, _ := os.UserHomeDir()
		legacyPath := filepath.Join(home, ".agents-mux", "config.yaml")
		if _, err := os.Stat(legacyPath); err == nil {
			path = legacyPath
		} else {
			path = filepath.Join(home, ".constellation", "config.yaml")
		}
	}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = yaml.Unmarshal(data, &cfg)
	}
	return cfg
}

func newManager() (*mux.Manager, error) {
	c := loadConfig()
	return mux.NewManager(mux.Config{
		DBPath:     c.DBPath,
		SessionDir: c.SessionDir,
		HandoffDir: c.HandoffDir,
	})
}

func exitErr(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
