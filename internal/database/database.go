package database

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"not-sale-back/internal/config"
	"not-sale-back/internal/models"
)

type Database struct {
	pool    *pgxpool.Pool
	queries map[string]string
}

func NewDatabase(cfg *config.Config) (*Database, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	poolConfig.MaxConns = int32(cfg.DBMaxOpenConns)
	poolConfig.MinConns = int32(cfg.DBMaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.DBConnMaxLifetime
	poolConfig.MaxConnIdleTime = cfg.DBConnMaxIdleTime

	poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement

	maxRetries := 5
	var lastErr error
	var pool *pgxpool.Pool

	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		cancel()

		if err == nil {
			if err = pool.Ping(context.Background()); err == nil {
				log.Println("Successfully connected to database")

				db := &Database{
					pool:    pool,
					queries: make(map[string]string),
				}

				if err := db.initializeQueries(); err != nil {
					pool.Close()
					return nil, fmt.Errorf("failed to initialize queries: %w", err)
				}

				return db, nil
			}
		}

		if pool != nil {
			pool.Close()
		}

		lastErr = err
		retryTime := time.Duration(1<<uint(i)) * time.Second
		log.Printf("Failed to connect to database, retrying in %v: %v", retryTime, err)
		time.Sleep(retryTime)
	}

	return nil, fmt.Errorf("failed to connect to database after %d retries: %w", maxRetries, lastErr)
}

func (d *Database) initializeQueries() error {
	d.queries = map[string]string{
		"getUserTotalItems":     "SELECT COUNT(*) FROM purchases WHERE user_id = $1 AND sale_id = $2",
		"checkItemAvailability": "SELECT EXISTS(SELECT 1 FROM items WHERE id = $1 AND sale_id = $2 AND sold = FALSE)",
		"markItemAsSold":        "UPDATE items SET sold = TRUE, owner_id = $3 WHERE id = $1 AND sale_id = $2 AND sold = FALSE",
		"recordPurchase":        "INSERT INTO purchases (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
		"updateSaleCounter":     "UPDATE flash_sales SET items_sold = items_sold + 1 WHERE id = $1",
	}

	return nil
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
			}
			continue
		}

		return err
	}

	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}

	if pgErr, ok := err.(*pgconn.PgError); ok {
		switch pgErr.Code {
		case "08000",
			"08003",
			"08006",
			"08001",
			"08004",
			"08007",
			"40001",
			"40P01",
			"XX000":
			return true
		}
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, "connection") {
		return strings.Contains(errMsg, "reset") ||
			strings.Contains(errMsg, "closed") ||
			strings.Contains(errMsg, "broken") ||
			strings.Contains(errMsg, "refused") ||
			strings.Contains(errMsg, "timeout")
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

	ctx := context.Background()
	for _, query := range queries {
		_, err := d.pool.Exec(ctx, query)
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

	ctx := context.Background()
	var sale models.FlashSale
	err := d.pool.QueryRow(ctx, "SELECT id, start_time, items_sold FROM flash_sales WHERE start_time = $1", startTime).
		Scan(&sale.ID, &sale.StartTime, &sale.ItemsSold)

	if errors.Is(err, pgx.ErrNoRows) {
		err = d.pool.QueryRow(ctx, "INSERT INTO flash_sales (start_time, items_sold) VALUES ($1, 0) RETURNING id, start_time, items_sold",
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
	ctx := context.Background()
	var count int
	err := d.pool.QueryRow(ctx, "SELECT COUNT(*) FROM items WHERE sale_id = $1", saleID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to check if items exist for sale: %w", err)
	}
	return count, nil
}

func (d *Database) GetUserTotalItems(ctx context.Context, userID string, saleID int) (int, error) {
	var purchaseCount int
	err := d.pool.QueryRow(ctx, d.queries["getUserTotalItems"], userID, saleID).Scan(&purchaseCount)
	if err != nil {
		return 0, fmt.Errorf("failed to count user purchases: %w", err)
	}

	return purchaseCount, nil
}

func (d *Database) StoreCheckoutAttempt(ctx context.Context, userID string, itemID int, code string, saleID int, status string) error {
	_, err := d.pool.Exec(ctx, "INSERT INTO checkout_attempts (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
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
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, "SELECT id, sale_id FROM items")
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
	d.pool.Close()
	return nil
}

func (d *Database) GetDB() interface{} {
	return d.pool
}

func (d *Database) GetActiveSales(ctx context.Context, limit int) ([]struct {
	ID        int
	StartTime time.Time
}, error) {
	rows, err := d.pool.Query(ctx, "SELECT id, start_time FROM flash_sales WHERE items_sold < $1 ORDER BY start_time DESC", limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query sales: %w", err)
	}
	defer rows.Close()

	var sales []struct {
		ID        int
		StartTime time.Time
	}

	for rows.Next() {
		var sale struct {
			ID        int
			StartTime time.Time
		}
		if err := rows.Scan(&sale.ID, &sale.StartTime); err != nil {
			return nil, fmt.Errorf("failed to scan sale: %w", err)
		}
		sales = append(sales, sale)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating through sales: %w", err)
	}

	return sales, nil
}

func (d *Database) BeginTx(ctx context.Context, isolationLevel string) (pgx.Tx, error) {
	return d.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.TxIsoLevel(isolationLevel),
	})
}

func (d *Database) CheckCodeUsed(ctx context.Context, tx pgx.Tx, code string) (bool, int, error) {
	var purchaseID int
	err := tx.QueryRow(ctx, "SELECT id FROM purchases WHERE code = $1", code).Scan(&purchaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("failed to check if code has been used: %w", err)
	}
	return true, purchaseID, nil
}

func (d *Database) GetSaleItemsSold(ctx context.Context, tx pgx.Tx, saleID int) (int, error) {
	var itemsSold int
	err := tx.QueryRow(ctx, "SELECT items_sold FROM flash_sales WHERE id = $1", saleID).Scan(&itemsSold)
	if err != nil {
		return 0, fmt.Errorf("failed to check sale status: %w", err)
	}
	return itemsSold, nil
}

func (d *Database) CheckItemAvailable(ctx context.Context, tx pgx.Tx, itemID, saleID int) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM items WHERE id = $1 AND sale_id = $2 AND sold = FALSE)",
		itemID, saleID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check if item exists: %w", err)
	}
	return exists, nil
}

func (d *Database) MarkItemAsSold(ctx context.Context, tx pgx.Tx, itemID, saleID int, userID string) (int64, error) {
	result, err := tx.Exec(ctx, "UPDATE items SET sold = TRUE, owner_id = $3 WHERE id = $1 AND sale_id = $2 AND sold = FALSE",
		itemID, saleID, userID)
	if err != nil {
		return 0, fmt.Errorf("failed to mark item as sold: %w", err)
	}
	return result.RowsAffected(), nil
}

func (d *Database) RecordPurchase(ctx context.Context, tx pgx.Tx, userID string, itemID int, code string, saleID int) error {
	_, err := tx.Exec(ctx, "INSERT INTO purchases (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
		userID, itemID, code, time.Now().UTC(), saleID)
	if err != nil {
		return fmt.Errorf("failed to record purchase: %w", err)
	}
	return nil
}

func (d *Database) UpdateSaleCounter(ctx context.Context, tx pgx.Tx, saleID int) error {
	_, err := tx.Exec(ctx, "UPDATE flash_sales SET items_sold = items_sold + 1 WHERE id = $1", saleID)
	if err != nil {
		return fmt.Errorf("failed to update sale: %w", err)
	}
	return nil
}

func (d *Database) GenerateItems(ctx context.Context, saleID int, itemNames []string, itemImages []string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for i := 0; i < len(itemNames); i++ {
		var itemID int
		err := tx.QueryRow(ctx,
			"INSERT INTO items (name, image, sale_id, sold) VALUES ($1, $2, $3, FALSE) RETURNING id",
			itemNames[i], itemImages[i], saleID).Scan(&itemID)
		if err != nil {
			return fmt.Errorf("failed to insert item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}
