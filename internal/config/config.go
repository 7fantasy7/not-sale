package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port string

	PostgresURL string
	RedisAddr   string

	// Database
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
	DBConnMaxIdleTime time.Duration

	// Redis
	RedisPoolSize     int
	RedisMinIdleConns int
	RedisMaxRetries   int
	RedisDialTimeout  time.Duration
	RedisReadTimeout  time.Duration
	RedisWriteTimeout time.Duration
	RedisPoolTimeout  time.Duration

	// HTTP
	ServerReadTimeout     time.Duration
	ServerWriteTimeout    time.Duration
	ServerIdleTimeout     time.Duration
	ServerShutdownTimeout time.Duration

	// Request handling
	RequestTimeout      time.Duration
	MaxConcurrentReqs   int
	EnableRequestLogger bool
}

func NewConfig() *Config {
	pgUser := getEnv("PG_USER", "postgres")
	pgPassword := getEnv("PG_PASSWORD", "postgres")
	pgHost := getEnv("PG_HOST", "localhost")
	pgPort := getEnv("PG_PORT", "5433")
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

		DBMaxOpenConns:    getEnvAsInt("DB_MAX_OPEN_CONNS", 100),
		DBMaxIdleConns:    getEnvAsInt("DB_MAX_IDLE_CONNS", 25),
		DBConnMaxLifetime: getEnvAsDuration("DB_CONN_MAX_LIFETIME", 15*time.Minute),
		DBConnMaxIdleTime: getEnvAsDuration("DB_CONN_MAX_IDLE_TIME", 5*time.Minute),

		RedisPoolSize:     getEnvAsInt("REDIS_POOL_SIZE", 100),
		RedisMinIdleConns: getEnvAsInt("REDIS_MIN_IDLE_CONNS", 10),
		RedisMaxRetries:   getEnvAsInt("REDIS_MAX_RETRIES", 3),
		RedisDialTimeout:  getEnvAsDuration("REDIS_DIAL_TIMEOUT", 5*time.Second),
		RedisReadTimeout:  getEnvAsDuration("REDIS_READ_TIMEOUT", 3*time.Second),
		RedisWriteTimeout: getEnvAsDuration("REDIS_WRITE_TIMEOUT", 3*time.Second),
		RedisPoolTimeout:  getEnvAsDuration("REDIS_POOL_TIMEOUT", 4*time.Second),

		ServerReadTimeout:     getEnvAsDuration("SERVER_READ_TIMEOUT", 5*time.Second),
		ServerWriteTimeout:    getEnvAsDuration("SERVER_WRITE_TIMEOUT", 10*time.Second),
		ServerIdleTimeout:     getEnvAsDuration("SERVER_IDLE_TIMEOUT", 120*time.Second),
		ServerShutdownTimeout: getEnvAsDuration("SERVER_SHUTDOWN_TIMEOUT", 20*time.Second),

		RequestTimeout:      getEnvAsDuration("REQUEST_TIMEOUT", 5*time.Second),
		MaxConcurrentReqs:   getEnvAsInt("MAX_CONCURRENT_REQS", 1000),
		EnableRequestLogger: getEnvAsBool("ENABLE_REQUEST_LOGGER", false),
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}

func getEnvAsDuration(key string, defaultValue time.Duration) time.Duration {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}

	value, err := time.ParseDuration(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}

func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}
