package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"

	"not-sale-back/internal/models"
)

type Database struct {
	db *sql.DB
}

func NewDatabase(connStr string) (*Database, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &Database{db: db}, nil
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
