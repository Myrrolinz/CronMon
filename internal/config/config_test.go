package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/myrrolinz/cronmon/internal/config"
)

// baseConfig returns a fully valid Config to mutate in subtests.
func baseConfig() config.Config {
	return config.Config{
		Port:              "8080",
		DBPath:            "cronmon.db",
		BaseURL:           "http://localhost:8080",
		SchedulerInterval: 30,
		AdminUser:         "admin",
		AdminPass:         "secret",
		SMTPHost:          "",
		SMTPPort:          "587",
		SMTPUser:          "",
		SMTPPass:          "",
		SMTPFrom:          "",
		SMTPTLS:           true,
		LogLevel:          "info",
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*config.Config)
		wantErr     bool
		errContains string
	}{
		// Valid baseline
		{
			name:    "valid config",
			mutate:  func(_ *config.Config) {},
			wantErr: false,
		},

		// BASE_URL rules
		{
			name:        "BASE_URL empty",
			mutate:      func(c *config.Config) { c.BaseURL = "" },
			wantErr:     true,
			errContains: "BASE_URL",
		},
		{
			name:        "BASE_URL not parseable",
			mutate:      func(c *config.Config) { c.BaseURL = "://not-a-url" },
			wantErr:     true,
			errContains: "BASE_URL",
		},
		{
			name:        "BASE_URL relative path only",
			mutate:      func(c *config.Config) { c.BaseURL = "/relative/path" },
			wantErr:     true,
			errContains: "BASE_URL",
		},

		// ADMIN_PASS rule
		{
			name:        "ADMIN_PASS empty",
			mutate:      func(c *config.Config) { c.AdminPass = "" },
			wantErr:     true,
			errContains: "ADMIN_PASS",
		},

		// SCHEDULER_INTERVAL rules
		{
			name:        "SCHEDULER_INTERVAL below minimum",
			mutate:      func(c *config.Config) { c.SchedulerInterval = 9 },
			wantErr:     true,
			errContains: "SCHEDULER_INTERVAL",
		},
		{
			name:        "SCHEDULER_INTERVAL zero",
			mutate:      func(c *config.Config) { c.SchedulerInterval = 0 },
			wantErr:     true,
			errContains: "SCHEDULER_INTERVAL",
		},
		{
			name:    "SCHEDULER_INTERVAL at minimum (10)",
			mutate:  func(c *config.Config) { c.SchedulerInterval = 10 },
			wantErr: false,
		},

		// SMTP partial-config rules
		{
			name: "SMTP_HOST set without SMTP_FROM",
			mutate: func(c *config.Config) {
				c.SMTPHost = "smtp.example.com"
				c.SMTPFrom = ""
			},
			wantErr:     true,
			errContains: "SMTP_FROM",
		},
		{
			name: "SMTP_FROM set without SMTP_HOST",
			mutate: func(c *config.Config) {
				c.SMTPHost = ""
				c.SMTPFrom = "noreply@example.com"
			},
			wantErr:     true,
			errContains: "SMTP_HOST",
		},
		{
			name: "SMTP_USER set without SMTP_HOST and SMTP_FROM",
			mutate: func(c *config.Config) {
				c.SMTPUser = "user"
				c.SMTPHost = ""
				c.SMTPFrom = ""
			},
			wantErr:     true,
			errContains: "SMTP_HOST",
		},
		{
			name: "SMTP_PASS set without SMTP_HOST and SMTP_FROM",
			mutate: func(c *config.Config) {
				c.SMTPPass = "pass"
				c.SMTPHost = ""
				c.SMTPFrom = ""
			},
			wantErr:     true,
			errContains: "SMTP_HOST",
		},
		{
			name: "full SMTP config is valid",
			mutate: func(c *config.Config) {
				c.SMTPHost = "smtp.example.com"
				c.SMTPFrom = "noreply@example.com"
				c.SMTPUser = "user"
				c.SMTPPass = "pass"
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()

			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Validate() error = %q, want it to contain %q", err.Error(), tt.errContains)
				}
			}
		})
	}
}

func TestString_RedactsPasswords(t *testing.T) {
	cfg := config.Config{
		AdminPass: "super-secret-admin",
		SMTPPass:  "super-secret-smtp",
	}
	s := cfg.String()

	if strings.Contains(s, "super-secret-admin") {
		t.Error("String() must not contain AdminPass value")
	}
	if strings.Contains(s, "super-secret-smtp") {
		t.Error("String() must not contain SMTPPass value")
	}
	if !strings.Contains(s, "***") {
		t.Error("String() must contain *** as the redacted placeholder")
	}
}

func TestString_EmptyPasswordsNotRedacted(t *testing.T) {
	cfg := baseConfig()
	cfg.AdminPass = ""
	cfg.SMTPPass = ""
	s := cfg.String()

	// When passwords are empty there is nothing to redact; *** must not appear
	// for the empty fields so operators can distinguish "not set" from "set".
	if strings.Contains(s, "***") {
		t.Error("String() must not show *** when passwords are empty")
	}
}

func TestLoad_MalformedDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("THIS IS NOT VALID\x00\xFF"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, err = config.Load()
	if err == nil {
		t.Fatal("Load() expected error for malformed .env, got nil")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error %q should mention .env", err.Error())
	}
}

// loadConfig changes into a temporary directory (without any .env file) before
// calling config.Load, so that developer-local .env files in the working
// directory cannot affect test results.
func loadConfig(t *testing.T) config.Config {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoad_InvalidSchedulerInterval(t *testing.T) {
	t.Setenv("SCHEDULER_INTERVAL", "not-a-number")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("ADMIN_PASS", "secret")
	cfg := loadConfig(t)
	if cfg.SchedulerInterval != -1 {
		t.Errorf("SchedulerInterval = %d, want -1 for non-numeric input", cfg.SchedulerInterval)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for non-numeric SCHEDULER_INTERVAL, got nil")
	}
	if !strings.Contains(err.Error(), "SCHEDULER_INTERVAL") {
		t.Errorf("error %q should mention SCHEDULER_INTERVAL", err.Error())
	}
}

func TestLoad_ReadsEnvVars(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DB_PATH", "/tmp/test.db")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("SCHEDULER_INTERVAL", "60")
	t.Setenv("ADMIN_USER", "testuser")
	t.Setenv("ADMIN_PASS", "testpass")
	t.Setenv("SMTP_HOST", "smtp.test.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_USER", "smtpuser")
	t.Setenv("SMTP_PASS", "smtppass")
	t.Setenv("SMTP_FROM", "from@test.com")
	t.Setenv("SMTP_TLS", "false")
	t.Setenv("TRUSTED_PROXY", "true")
	t.Setenv("REQUIRE_HTTPS", "true")
	t.Setenv("LOG_LEVEL", "debug")

	cfg := loadConfig(t)

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"Port", cfg.Port, "9090"},
		{"DBPath", cfg.DBPath, "/tmp/test.db"},
		{"BaseURL", cfg.BaseURL, "https://example.com"},
		{"AdminUser", cfg.AdminUser, "testuser"},
		{"AdminPass", cfg.AdminPass, "testpass"},
		{"SMTPHost", cfg.SMTPHost, "smtp.test.com"},
		{"SMTPPort", cfg.SMTPPort, "465"},
		{"SMTPUser", cfg.SMTPUser, "smtpuser"},
		{"SMTPPass", cfg.SMTPPass, "smtppass"},
		{"SMTPFrom", cfg.SMTPFrom, "from@test.com"},
		{"LogLevel", cfg.LogLevel, "debug"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if cfg.SchedulerInterval != 60 {
		t.Errorf("SchedulerInterval = %d, want 60", cfg.SchedulerInterval)
	}
	if cfg.SMTPTLS != false {
		t.Errorf("SMTPTLS = %v, want false", cfg.SMTPTLS)
	}
	if cfg.TrustedProxy != true {
		t.Errorf("TrustedProxy = %v, want true", cfg.TrustedProxy)
	}
	if cfg.RequireHTTPS != true {
		t.Errorf("RequireHTTPS = %v, want true", cfg.RequireHTTPS)
	}
}

// unsetenv removes key for the duration of the test and restores its prior
// value (or removes it again) via t.Cleanup.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	if prev, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { os.Setenv(key, prev) }) //nolint:errcheck
	} else {
		t.Cleanup(func() { os.Unsetenv(key) }) //nolint:errcheck
	}
	os.Unsetenv(key) //nolint:errcheck
}

func TestLoad_Defaults(t *testing.T) {
	// Unset all vars that have defaults so we can verify fallback values.
	for _, key := range []string{
		"PORT", "DB_PATH", "SCHEDULER_INTERVAL", "ADMIN_USER",
		"SMTP_PORT", "SMTP_TLS", "LOG_LEVEL",
		"BASE_URL", "ADMIN_PASS", "SMTP_HOST", "SMTP_USER",
		"SMTP_PASS", "SMTP_FROM", "TRUSTED_PROXY", "REQUIRE_HTTPS",
	} {
		unsetenv(t, key)
	}

	cfg := loadConfig(t)

	defaults := []struct {
		field string
		got   string
		want  string
	}{
		{"Port", cfg.Port, "8080"},
		{"DBPath", cfg.DBPath, "cronmon.db"},
		{"AdminUser", cfg.AdminUser, "admin"},
		{"SMTPPort", cfg.SMTPPort, "587"},
		{"LogLevel", cfg.LogLevel, "info"},
	}
	for _, d := range defaults {
		if d.got != d.want {
			t.Errorf("default %s = %q, want %q", d.field, d.got, d.want)
		}
	}
	if cfg.SchedulerInterval != 30 {
		t.Errorf("default SchedulerInterval = %d, want 30", cfg.SchedulerInterval)
	}
	if cfg.SMTPTLS != true {
		t.Errorf("default SMTPTLS = %v, want true", cfg.SMTPTLS)
	}
}
