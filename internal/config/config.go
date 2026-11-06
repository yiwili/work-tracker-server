package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr             string
	DatabaseURL          string
	DBMaxConns           int32
	AllowRegistration    bool
	CloudPublishEnabled  bool
	ChangeRetentionDays  int
	PublishPreviewTTL    time.Duration
	SendingStaleAfter    time.Duration
	SweepInterval        time.Duration
	BcryptCost           int
	LogLevel             string
	TrustedProxies       []string
	MaxBodyBytes         int64
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:            getEnv("WTS_HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("WTS_DATABASE_URL"),
		DBMaxConns:          4,
		AllowRegistration:   true,
		CloudPublishEnabled: false,
		ChangeRetentionDays: 90,
		PublishPreviewTTL:   15 * time.Minute,
		SendingStaleAfter:   10 * time.Minute,
		SweepInterval:       1 * time.Minute,
		BcryptCost:          12,
		LogLevel:            getEnv("WTS_LOG_LEVEL", "info"),
		MaxBodyBytes:        4194304,
	}

	if val := os.Getenv("WTS_DB_MAX_CONNS"); val != "" {
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid WTS_DB_MAX_CONNS: %s", val)
		}
		c.DBMaxConns = int32(n)
	}

	if val := os.Getenv("WTS_ALLOW_REGISTRATION"); val != "" {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return Config{}, fmt.Errorf("invalid WTS_ALLOW_REGISTRATION: %s", val)
		}
		c.AllowRegistration = b
	}

	if val := os.Getenv("WTS_CLOUD_PUBLISH_ENABLED"); val != "" {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return Config{}, fmt.Errorf("invalid WTS_CLOUD_PUBLISH_ENABLED: %s", val)
		}
		c.CloudPublishEnabled = b
	}

	if val := os.Getenv("WTS_CHANGE_RETENTION_DAYS"); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid WTS_CHANGE_RETENTION_DAYS: %s", val)
		}
		c.ChangeRetentionDays = n
	}

	if val := os.Getenv("WTS_PUBLISH_PREVIEW_TTL"); val != "" {
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid WTS_PUBLISH_PREVIEW_TTL: %s", val)
		}
		c.PublishPreviewTTL = d
	}

	if val := os.Getenv("WTS_SENDING_STALE_AFTER"); val != "" {
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid WTS_SENDING_STALE_AFTER: %s", val)
		}
		c.SendingStaleAfter = d
	}

	if val := os.Getenv("WTS_SWEEP_INTERVAL"); val != "" {
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid WTS_SWEEP_INTERVAL: %s", val)
		}
		c.SweepInterval = d
	}

	if val := os.Getenv("WTS_BCRYPT_COST"); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil || n < 4 || n > 31 {
			return Config{}, fmt.Errorf("invalid WTS_BCRYPT_COST (must be 4..31): %s", val)
		}
		c.BcryptCost = n
	}

	if val := os.Getenv("WTS_MAX_BODY_BYTES"); val != "" {
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid WTS_MAX_BODY_BYTES: %s", val)
		}
		c.MaxBodyBytes = n
	}

	if val := os.Getenv("WTS_TRUSTED_PROXIES"); val != "" {
		parts := strings.Split(val, ",")
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				c.TrustedProxies = append(c.TrustedProxies, s)
			}
		}
	}

	return c, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
