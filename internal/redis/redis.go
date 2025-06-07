package redis

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisClient struct {
	client *redis.Client
}

func NewRedisClient(addr string) (*RedisClient, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: "",
		DB:       0,
	})

	ctx := context.Background()
	if _, err := client.Ping(ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to ping Redis: %w", err)
	}

	return &RedisClient{client: client}, nil
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
