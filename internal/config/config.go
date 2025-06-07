package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port        string
	PostgresURL string
	RedisAddr   string
}

func NewConfig() *Config {
	pgUser := getEnv("PG_USER", "postgres")
	pgPassword := getEnv("PG_PASSWORD", "postgres")
	pgHost := getEnv("PG_HOST", "localhost")
	pgPort := getEnv("PG_PORT", "5432")
	pgDB := getEnv("PG_DB", "app")

	postgresURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		pgUser, pgPassword, pgHost, pgPort, pgDB)

	redisHost := getEnv("REDIS_HOST", "localhost")
	redisPort := getEnv("REDIS_PORT", "6379")
	redisAddr := fmt.Sprintf("%s:%s", redisHost, redisPort)

	return &Config{
		Port:        getEnv("PORT", "8080"),
		PostgresURL: postgresURL,
		RedisAddr:   redisAddr,
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
