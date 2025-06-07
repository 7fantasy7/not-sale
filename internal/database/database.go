package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"not-sale-back/internal/config"
	"not-sale-back/internal/models"
)

type Database struct {
	db *sql.DB
}

func NewDatabase(cfg *config.Config) (*Database, error) {
	db, err := sql.Open("postgres", cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)

	maxRetries := 5
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		err := db.Ping()
		if err == nil {
			log.Println("Successfully connected to database")
			return &Database{db: db}, nil
		}
		lastErr = err
		retryTime := time.Duration(1<<uint(i)) * time.Second
		log.Printf("Failed to ping database, retrying in %v: %v", retryTime, err)
		time.Sleep(retryTime)
	}

	return nil, fmt.Errorf("failed to ping database after %d retries: %w", maxRetries, lastErr)
}

func (d *Database) ExecuteWithRetry(ctx context.Context, operation func() error) error {
	maxRetries := 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := operation()
		if err == nil {
			return nil
		}

		if isTransientError(err) {
			lastErr = err
			retryTime := time.Duration(1<<uint(i)) * 100 * time.Millisecond
			log.Printf("Transient database error, retrying in %v: %v", retryTime, err)

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

	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

func isTransientError(err error) bool {
	if err != nil {
		errMsg := err.Error()

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return true
		}
		if errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) {
			return true
		}
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}

		if strings.Contains(errMsg, "connection") {
			return strings.Contains(errMsg, "reset") ||
				strings.Contains(errMsg, "closed") ||
				strings.Contains(errMsg, "broken") ||
				strings.Contains(errMsg, "refused") ||
				strings.Contains(errMsg, "timeout")
		}
	}
	return false
}

func (d *Database) InitDB() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS flash_sales (
			id SERIAL PRIMARY KEY,
			start_time TIMESTAMP WITH TIME ZONE NOT NULL,
			items_sold INT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS items (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			image TEXT NOT NULL,
			sale_id INT REFERENCES flash_sales(id),
			sold BOOLEAN NOT NULL DEFAULT FALSE,
			owner_id TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS checkout_attempts (
			id SERIAL PRIMARY KEY,
			user_id TEXT NOT NULL,
			item_id INT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL,
			sale_id INT REFERENCES flash_sales(id)
		)`,
		`CREATE TABLE IF NOT EXISTS purchases (
			id SERIAL PRIMARY KEY,
			user_id TEXT NOT NULL,
			item_id INT NOT NULL,
			code TEXT NOT NULL REFERENCES checkout_attempts(code),
			created_at TIMESTAMP WITH TIME ZONE NOT NULL,
			sale_id INT REFERENCES flash_sales(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_checkout_attempts_code ON checkout_attempts(code)`,
		`CREATE INDEX IF NOT EXISTS idx_checkout_attempts_user_id_sale_id ON checkout_attempts(user_id, sale_id)`,
		`CREATE INDEX IF NOT EXISTS idx_purchases_user_id_sale_id ON purchases(user_id, sale_id)`,
	}

	for _, query := range queries {
		_, err := d.db.Exec(query)
		if err != nil {
			return fmt.Errorf("failed to execute query: %s, error: %w", query, err)
		}
	}

	return nil
}

func (d *Database) GetCurrentSale() (*models.FlashSale, error) {
	now := time.Now().UTC()
	startTime := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.UTC)

	if now.After(startTime.Add(time.Hour)) {
		startTime = startTime.Add(time.Hour)
	}

	var sale models.FlashSale
	err := d.db.QueryRow("SELECT id, start_time, items_sold FROM flash_sales WHERE start_time = $1", startTime).
		Scan(&sale.ID, &sale.StartTime, &sale.ItemsSold)

	if errors.Is(err, sql.ErrNoRows) {
		err = d.db.QueryRow("INSERT INTO flash_sales (start_time, items_sold) VALUES ($1, 0) RETURNING id, start_time, items_sold",
			startTime).Scan(&sale.ID, &sale.StartTime, &sale.ItemsSold)
		if err != nil {
			return nil, fmt.Errorf("failed to create new sale: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to query current sale: %w", err)
	}

	return &sale, nil
}

func (d *Database) GetItemsForSale(saleID int) (int, error) {
	var count int
	err := d.db.QueryRow("SELECT COUNT(*) FROM items WHERE sale_id = $1", saleID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to check if items exist for sale: %w", err)
	}
	return count, nil
}

func (d *Database) GetUserTotalItems(ctx context.Context, userID string, saleID int) (int, error) {
	var purchaseCount int
	err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM purchases WHERE user_id = $1 AND sale_id = $2", userID, saleID).Scan(&purchaseCount)
	if err != nil {
		return 0, fmt.Errorf("failed to count user purchases: %w", err)
	}

	return purchaseCount, nil
}

func (d *Database) StoreCheckoutAttempt(ctx context.Context, userID string, itemID int, code string, saleID int, status string) error {
	_, err := d.db.ExecContext(ctx, "INSERT INTO checkout_attempts (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
		userID, itemID, code, time.Now().UTC(), saleID)
	if err != nil {
		return fmt.Errorf("failed to store checkout attempt: %w", err)
	}
	return nil
}

func (d *Database) GetAllItems() ([]struct {
	ID     int
	SaleID int
}, error) {
	rows, err := d.db.Query("SELECT id, sale_id FROM items")
	if err != nil {
		return nil, fmt.Errorf("failed to query items: %w", err)
	}
	defer rows.Close()

	var items []struct {
		ID     int
		SaleID int
	}

	for rows.Next() {
		var item struct {
			ID     int
			SaleID int
		}
		if err := rows.Scan(&item.ID, &item.SaleID); err != nil {
			return nil, fmt.Errorf("failed to scan item row: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating through items: %w", err)
	}

	return items, nil
}

func (d *Database) Close() error {
	if err := d.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}
	return nil
}

func (d *Database) GetDB() *sql.DB {
	return d.db
}
