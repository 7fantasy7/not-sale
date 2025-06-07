package redis

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"not-sale-back/internal/config"
)

type RedisClient struct {
	client *redis.Client
}

func NewRedisClient(cfg *config.Config) (*RedisClient, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     "",
		DB:           0,
		PoolSize:     cfg.RedisPoolSize,
		MinIdleConns: cfg.RedisMinIdleConns,
		MaxRetries:   cfg.RedisMaxRetries,
		DialTimeout:  cfg.RedisDialTimeout,
		ReadTimeout:  cfg.RedisReadTimeout,
		WriteTimeout: cfg.RedisWriteTimeout,
		PoolTimeout:  cfg.RedisPoolTimeout,
	})

	maxRetries := 5
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := client.Ping(ctx).Result()
		cancel()

		if err == nil {
			log.Println("Successfully connected to Redis")
			return &RedisClient{client: client}, nil
		}

		lastErr = err
		retryTime := time.Duration(1<<uint(i)) * time.Second
		log.Printf("Failed to ping Redis, retrying in %v: %v", retryTime, err)
		time.Sleep(retryTime)
	}

	return nil, fmt.Errorf("failed to ping Redis after %d retries: %w", maxRetries, lastErr)
}

func (r *RedisClient) ExecuteWithRetry(ctx context.Context, operation func(context.Context) error) error {
	maxRetries := 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := operation(ctx)
		if err == nil {
			return nil
		}

		if isTransientRedisError(err) {
			lastErr = err
			retryTime := time.Duration(1<<uint(i)) * 100 * time.Millisecond
			log.Printf("Transient Redis error, retrying in %v: %v", retryTime, err)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryTime):
				// Continue
			}
			continue
		}

		return err
	}

	return fmt.Errorf("redis operation failed after %d retries: %w", maxRetries, lastErr)
}

func isTransientRedisError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}

	errMsg := err.Error()
	return errMsg == "redis: connection pool timeout" ||
		errMsg == "redis: connection closed" ||
		errMsg == "redis: client is closed" ||
		errMsg == "i/o timeout" ||
		errMsg == "connection refused" ||
		errMsg == "connection reset by peer"
}

func (r *RedisClient) StoreItem(ctx context.Context, saleID, itemID int, expiration time.Duration) error {
	itemKey := fmt.Sprintf("item:%d:%d", saleID, itemID)
	err := r.client.Set(ctx, itemKey, "1", expiration).Err()
	if err != nil {
		return fmt.Errorf("failed to store item in Redis: %w", err)
	}
	return nil
}

func (r *RedisClient) ItemExists(ctx context.Context, saleID, itemID int) (bool, error) {
	itemKey := fmt.Sprintf("item:%d:%d", saleID, itemID)
	exists, err := r.client.Exists(ctx, itemKey).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check if item exists in Redis: %w", err)
	}
	return exists == 1, nil
}

func (r *RedisClient) StoreCode(ctx context.Context, code, data string, expiration time.Duration) error {
	err := r.client.Set(ctx, "code:"+code, data, expiration).Err()
	if err != nil {
		return fmt.Errorf("failed to store code in Redis: %w", err)
	}
	return nil
}

func (r *RedisClient) GetCode(ctx context.Context, code string) (string, error) {
	data, err := r.client.Get(ctx, "code:"+code).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("code not found or expired")
	} else if err != nil {
		return "", fmt.Errorf("failed to get code from Redis: %w", err)
	}
	return data, nil
}

func (r *RedisClient) DeleteCode(ctx context.Context, code string) error {
	err := r.client.Del(ctx, "code:"+code).Err()
	if err != nil {
		return fmt.Errorf("failed to delete code from Redis: %w", err)
	}
	return nil
}

func (r *RedisClient) PopulateItemsCache(ctx context.Context, items []struct {
	ID     int
	SaleID int
}, expiration time.Duration) error {
	log.Println("Populating Redis cache with existing items...")
	count := 0

	for _, item := range items {
		itemKey := fmt.Sprintf("item:%d:%d", item.SaleID, item.ID)
		err := r.client.Set(ctx, itemKey, "1", expiration).Err()
		if err != nil {
			log.Printf("Failed to store item in Redis: %v", err)
		} else {
			count++
		}
	}

	log.Printf("Successfully populated Redis cache with %d items", count)
	return nil
}

func (r *RedisClient) Close() error {
	if err := r.client.Close(); err != nil {
		return fmt.Errorf("failed to close Redis: %w", err)
	}
	return nil
}

func (r *RedisClient) GetClient() *redis.Client {
	return r.client
}
