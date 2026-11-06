package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/config"
)

func TestConfigDefaults(t *testing.T) {
	os.Clearenv()
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, int32(4), cfg.DBMaxConns)
	require.True(t, cfg.AllowRegistration)
	require.False(t, cfg.CloudPublishEnabled)
	require.Equal(t, 90, cfg.ChangeRetentionDays)
	require.Equal(t, 15*time.Minute, cfg.PublishPreviewTTL)
	require.Equal(t, 10*time.Minute, cfg.SendingStaleAfter)
	require.Equal(t, 1*time.Minute, cfg.SweepInterval)
	require.Equal(t, 12, cfg.BcryptCost)
	require.Equal(t, int64(4194304), cfg.MaxBodyBytes)
}

func TestConfigOverrides(t *testing.T) {
	os.Setenv("WTS_HTTP_ADDR", ":9090")
	os.Setenv("WTS_DB_MAX_CONNS", "12")
	os.Setenv("WTS_ALLOW_REGISTRATION", "false")
	os.Setenv("WTS_CLOUD_PUBLISH_ENABLED", "true")
	os.Setenv("WTS_BCRYPT_COST", "4")
	os.Setenv("WTS_TRUSTED_PROXIES", "127.0.0.1, 10.0.0.0/8")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, ":9090", cfg.HTTPAddr)
	require.Equal(t, int32(12), cfg.DBMaxConns)
	require.False(t, cfg.AllowRegistration)
	require.True(t, cfg.CloudPublishEnabled)
	require.Equal(t, 4, cfg.BcryptCost)
	require.Equal(t, []string{"127.0.0.1", "10.0.0.0/8"}, cfg.TrustedProxies)
}
