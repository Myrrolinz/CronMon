package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	Port              string
	DBPath            string
	BaseURL           string
	SchedulerInterval int
	AdminUser         string
	AdminPass         string
	SMTPHost          string
	SMTPPort          string
	SMTPUser          string
	SMTPPass          string
	SMTPFrom          string
	SMTPTLS           bool
	TrustedProxy      bool
	RequireHTTPS      bool
	LogLevel          string
}

// Load reads configuration from environment variables.
// It silently skips a .env file if it is absent.
func Load() Config {
	_ = godotenv.Load() // silently skip if absent

	interval, err := strconv.Atoi(getenv("SCHEDULER_INTERVAL", "30"))
	if err != nil {
		interval = -1
	}

	return Config{
		Port:              getenv("PORT", "8080"),
		DBPath:            getenv("DB_PATH", "cronmon.db"),
		BaseURL:           os.Getenv("BASE_URL"),
		SchedulerInterval: interval,
		AdminUser:         getenv("ADMIN_USER", "admin"),
		AdminPass:         os.Getenv("ADMIN_PASS"),
		SMTPHost:          os.Getenv("SMTP_HOST"),
		SMTPPort:          getenv("SMTP_PORT", "587"),
		SMTPUser:          os.Getenv("SMTP_USER"),
		SMTPPass:          os.Getenv("SMTP_PASS"),
		SMTPFrom:          os.Getenv("SMTP_FROM"),
		SMTPTLS:           getenv("SMTP_TLS", "true") != "false",
		TrustedProxy:      os.Getenv("TRUSTED_PROXY") == "true",
		RequireHTTPS:      os.Getenv("REQUIRE_HTTPS") == "true",
		LogLevel:          getenv("LOG_LEVEL", "info"),
	}
}

// Validate enforces startup rules. Returns the first validation error encountered.
func (c Config) Validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("BASE_URL is required")
	}
	parsed, err := url.ParseRequestURI(c.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("BASE_URL must be a valid absolute URL with scheme and host (e.g. https://cronmon.example.com)")
	}

	if c.AdminPass == "" {
		return fmt.Errorf("ADMIN_PASS is required")
	}

	if c.SchedulerInterval < 0 {
		return fmt.Errorf("SCHEDULER_INTERVAL must be a valid integer >= 10")
	}
	if c.SchedulerInterval < 10 {
		return fmt.Errorf("SCHEDULER_INTERVAL must be >= 10, got %d", c.SchedulerInterval)
	}

	// If any SMTP_* variable is set, both SMTP_HOST and SMTP_FROM must be present.
	smtpAnySet := c.SMTPHost != "" || c.SMTPUser != "" || c.SMTPPass != "" || c.SMTPFrom != ""
	if smtpAnySet {
		if c.SMTPHost == "" {
			return fmt.Errorf("SMTP_HOST must be set when any SMTP_* variable is configured")
		}
		if c.SMTPFrom == "" {
			return fmt.Errorf("SMTP_FROM must be set when any SMTP_* variable is configured")
		}
	}

	return nil
}

// String returns a human-readable representation of the config.
// AdminPass and SMTPPass are redacted and never appear in the output.
func (c Config) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Config{\n")
	fmt.Fprintf(&sb, "  Port:              %s\n", c.Port)
	fmt.Fprintf(&sb, "  DBPath:            %s\n", c.DBPath)
	fmt.Fprintf(&sb, "  BaseURL:           %s\n", c.BaseURL)
	fmt.Fprintf(&sb, "  SchedulerInterval: %d\n", c.SchedulerInterval)
	fmt.Fprintf(&sb, "  AdminUser:         %s\n", c.AdminUser)
	fmt.Fprintf(&sb, "  AdminPass:         %s\n", redact(c.AdminPass))
	fmt.Fprintf(&sb, "  SMTPHost:          %s\n", c.SMTPHost)
	fmt.Fprintf(&sb, "  SMTPPort:          %s\n", c.SMTPPort)
	fmt.Fprintf(&sb, "  SMTPUser:          %s\n", c.SMTPUser)
	fmt.Fprintf(&sb, "  SMTPPass:          %s\n", redact(c.SMTPPass))
	fmt.Fprintf(&sb, "  SMTPFrom:          %s\n", c.SMTPFrom)
	fmt.Fprintf(&sb, "  SMTPTLS:           %v\n", c.SMTPTLS)
	fmt.Fprintf(&sb, "  TrustedProxy:      %v\n", c.TrustedProxy)
	fmt.Fprintf(&sb, "  RequireHTTPS:      %v\n", c.RequireHTTPS)
	fmt.Fprintf(&sb, "  LogLevel:          %s\n", c.LogLevel)
	fmt.Fprintf(&sb, "}")
	return sb.String()
}

// getenv returns the value of the environment variable named by key.
// If the variable is not set, it returns defaultVal.
func getenv(key, defaultVal string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return defaultVal
}

// redact returns "***" for non-empty secrets and "" for unset ones,
// allowing operators to distinguish "configured" from "not configured".
func redact(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}
