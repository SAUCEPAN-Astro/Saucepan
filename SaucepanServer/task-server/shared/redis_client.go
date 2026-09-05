package shared

import (
	"fmt"
	"os"

	"github.com/go-redis/redis/v8"
)

// RedisOptionsFromEnv builds client options from REDIS_URL, or REDIS_ADDR + REDIS_PASSWORD.
// When DEV_MODE is not "1", a password is required (AUTH defense-in-depth on the compose network).
func RedisOptionsFromEnv() (*redis.Options, error) {
	if u := os.Getenv("REDIS_URL"); u != "" {
		opt, err := redis.ParseURL(u)
		if err != nil {
			return nil, fmt.Errorf("REDIS_URL: %w", err)
		}
		if opt.Password == "" && os.Getenv("DEV_MODE") != "1" {
			return nil, fmt.Errorf("REDIS_URL must include a password when DEV_MODE is not 1")
		}
		return opt, nil
	}

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	password := os.Getenv("REDIS_PASSWORD")
	if password == "" && os.Getenv("DEV_MODE") != "1" {
		return nil, fmt.Errorf("REDIS_PASSWORD is required when DEV_MODE is not 1")
	}
	return &redis.Options{Addr: addr, Password: password}, nil
}
