package config

// Config holds all application configuration.
type Config struct {
	Kubeconfig      string
	Port            int
	MetricInterval  int
	InsightInterval int
	CORSOrigins     []string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Port:            8080,
		MetricInterval:  30,
		InsightInterval: 60,
		CORSOrigins:     []string{"http://localhost:3000", "http://localhost:5173"},
	}
}
