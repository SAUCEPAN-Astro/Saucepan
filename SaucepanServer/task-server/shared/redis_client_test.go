package shared

import (
	"testing"
)

func TestRedisOptionsFromEnvRequiresPasswordOutsideDev(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("DEV_MODE", "")

	if _, err := RedisOptionsFromEnv(); err == nil {
		t.Fatal("expected error when REDIS_PASSWORD missing outside DEV_MODE")
	}

	t.Setenv("DEV_MODE", "1")
	opt, err := RedisOptionsFromEnv()
	if err != nil {
		t.Fatalf("DEV_MODE=1 should allow empty password: %v", err)
	}
	if opt.Addr != "redis:6379" || opt.Password != "" {
		t.Fatalf("got addr=%q password=%q", opt.Addr, opt.Password)
	}
}

func TestRedisOptionsFromEnvPasswordAndURL(t *testing.T) {
	t.Setenv("DEV_MODE", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_PASSWORD", "s3cret")

	opt, err := RedisOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opt.Password != "s3cret" || opt.Addr != "redis:6379" {
		t.Fatalf("got addr=%q password=%q", opt.Addr, opt.Password)
	}

	t.Setenv("REDIS_URL", "redis://:s3cret@redis:6379/0")
	opt, err = RedisOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opt.Password != "s3cret" || opt.Addr != "redis:6379" {
		t.Fatalf("URL parse: addr=%q password=%q", opt.Addr, opt.Password)
	}
}
