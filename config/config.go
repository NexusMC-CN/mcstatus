package config

import (
	"os"
	"regexp"
	"strconv"
	"time"
)

type Config struct {
	ListenAddress string
	APIToken      string
	Workers       int
	QueueSize     int
	Timeout       time.Duration
	JobTTL        time.Duration
}

func Load() Config {
	token := os.Getenv("MCSTATUS_API_TOKEN")
	if token != "" && !regexp.MustCompile(`^[\x21-\x7e]{32,512}$`).MatchString(token) {
		panic("MCSTATUS_API_TOKEN must contain 32 to 512 printable ASCII characters without whitespace")
	}
	return Config{
		APIToken:      token,
		ListenAddress: envString("MCSTATUS_LISTEN_ADDRESS", "127.0.0.1:4420"),
		Workers:       envInt("MCSTATUS_WORKERS", 8, 1, 64),
		QueueSize:     envInt("MCSTATUS_QUEUE_SIZE", 256, 1, 4096),
		Timeout:       time.Duration(envInt("MCSTATUS_TIMEOUT_MS", 4000, 1000, 30000)) * time.Millisecond,
		JobTTL:        time.Duration(envInt("MCSTATUS_JOB_TTL_MINUTES", 15, 1, 1440)) * time.Minute,
	}
}

func envString(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func envInt(name string, fallback int, min int, max int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}
