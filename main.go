package main

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

const (
	FlashSaleSize      = 10000
	MaxItemsPerUser    = 10
	CodeExpirationTime = 1 * time.Hour
)

type FlashSale struct {
	ID        int       `json:"id"`
	StartTime time.Time `json:"start_time"`
	ItemsSold int       `json:"items_sold"`
}

type App struct {
	db        *sql.DB
	rdb       *redis.Client
	router    *chi.Mux
	saleMutex sync.RWMutex
}

func NewApp() (*App, error) {
	connStr := "postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	ctx := context.Background()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to ping Redis: %w", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	app := &App{
		db:     db,
		rdb:    rdb,
		router: r,
	}

	if err := app.initDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	app.routes()

	return app, nil
}

func (a *App) initDB() error {
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
		_, err := a.db.Exec(query)
		if err != nil {
			return fmt.Errorf("failed to execute query: %s, error: %w", query, err)
		}
	}

	if err := a.populateItemsCache(); err != nil {
		log.Printf("Warning: failed to populate items cache: %v", err)
	}

	return nil
}

func (a *App) populateItemsCache() error {
	log.Println("Populating Redis cache with existing items...")

	rows, err := a.db.Query("SELECT id, sale_id FROM items")
	if err != nil {
		return fmt.Errorf("failed to query items: %w", err)
	}
	defer rows.Close()

	ctx := context.Background()
	count := 0

	for rows.Next() {
		var itemID, saleID int
		if err := rows.Scan(&itemID, &saleID); err != nil {
			return fmt.Errorf("failed to scan item row: %w", err)
		}

		itemKey := fmt.Sprintf("item:%d:%d", saleID, itemID)
		err = a.rdb.Set(ctx, itemKey, "1", time.Hour).Err()
		if err != nil {
			log.Printf("Failed to store item in Redis: %v", err)
		} else {
			count++
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating through items: %w", err)
	}

	log.Printf("Successfully populated Redis cache with %d items", count)
	return nil
}

func (a *App) routes() {
	a.router.Post("/checkout", a.handleCheckout)
	a.router.Post("/purchase", a.handlePurchase)
}

func (a *App) handleCheckout(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	itemIDStr := r.URL.Query().Get("id")
	ctx := r.Context()

	code, err := generateUniqueCode()
	if err != nil {
		http.Error(w, "Failed to generate code", http.StatusInternalServerError)
		log.Printf("Failed to generate code: %v", err)
		return
	}

	if userID == "" || itemIDStr == "" {
		if err := a.storeCheckoutAttempt(ctx, userID, 0, code, 0, "error_missing_params"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Missing user_id or id parameter", http.StatusBadRequest)
		return
	}

	itemID, err := strconv.Atoi(itemIDStr)
	if err != nil {
		if err := a.storeCheckoutAttempt(ctx, userID, 0, code, 0, "error_invalid_item_id"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Invalid item ID", http.StatusBadRequest)
		return
	}

	rows, err := a.db.QueryContext(ctx, "SELECT id, start_time FROM flash_sales WHERE items_sold < $1 ORDER BY start_time DESC", FlashSaleSize)
	if err != nil {
		if err := a.storeCheckoutAttempt(ctx, userID, itemID, code, 0, "error_query_sales"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Failed to query sales", http.StatusInternalServerError)
		log.Printf("Failed to query sales: %v", err)
		return
	}
	defer rows.Close()

	var saleID int
	var startTime time.Time
	var itemFound bool
	var foundSaleID int

	for rows.Next() {
		if err := rows.Scan(&saleID, &startTime); err != nil {
			if err := a.storeCheckoutAttempt(ctx, userID, itemID, code, 0, "error_scan_sale_id"); err != nil {
				log.Printf("Failed to store checkout attempt: %v", err)
			}
			http.Error(w, "Failed to scan sale ID", http.StatusInternalServerError)
			log.Printf("Failed to scan sale ID: %v", err)
			return
		}

		now := time.Now().UTC()
		if now.Before(startTime) {
			continue
		}

		available, err := a.isItemAvailable(ctx, itemID, saleID)
		if err == nil && available {
			itemFound = true
			foundSaleID = saleID
			break
		}
	}

	if !itemFound {
		if err := a.storeCheckoutAttempt(ctx, userID, itemID, code, 0, "error_item_not_found"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Item not found in any active sale", http.StatusNotFound)
		return
	}

	if err := a.storeCheckoutAttempt(ctx, userID, itemID, code, foundSaleID, "success"); err != nil {
		http.Error(w, "Failed to store checkout attempt", http.StatusInternalServerError)
		log.Printf("Failed to store checkout attempt: %v", err)
		return
	}

	if err := a.rdb.Set(ctx, "code:"+code, fmt.Sprintf("%s:%d:%d", userID, itemID, foundSaleID), CodeExpirationTime).Err(); err != nil {
		http.Error(w, "Failed to store code", http.StatusInternalServerError)
		log.Printf("Failed to store code in Redis: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func (a *App) handlePurchase(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")

	if code == "" {
		http.Error(w, "Missing code parameter", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	codeData, err := a.rdb.Get(ctx, "code:"+code).Result()
	if err == redis.Nil {
		http.Error(w, "Invalid or expired code", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "Failed to verify code", http.StatusInternalServerError)
		log.Printf("Failed to get code from Redis: %v", err)
		return
	}

	parts := strings.SplitN(codeData, ":", 3)
	if len(parts) != 3 {
		http.Error(w, "Failed to parse code data", http.StatusInternalServerError)
		log.Printf("Failed to parse code data: %v", codeData)
		return
	}
	userID := parts[0]
	itemID, err := strconv.Atoi(parts[1])
	if err != nil {
		http.Error(w, "Invalid item ID in code data", http.StatusInternalServerError)
		return
	}
	saleID, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "Invalid sale ID in code data", http.StatusInternalServerError)
		return
	}

	var saleExists bool
	err = a.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM flash_sales WHERE id = $1)", saleID).Scan(&saleExists)
	if err != nil {
		http.Error(w, "Failed to check if sale exists", http.StatusInternalServerError)
		log.Printf("Failed to check if sale exists: %v", err)
		return
	}

	if !saleExists {
		http.Error(w, "Sale does not exist", http.StatusBadRequest)
		return
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "Failed to begin transaction", http.StatusInternalServerError)
		log.Printf("Failed to begin transaction: %v", err)
		return
	}
	defer tx.Rollback()

	var purchaseID int
	err = tx.QueryRowContext(ctx, "SELECT id FROM purchases WHERE code = $1", code).Scan(&purchaseID)
	if err == nil {
		http.Error(w, "Code has already been used", http.StatusConflict)
		return
	} else if err != sql.ErrNoRows {
		http.Error(w, "Failed to check if code has been used", http.StatusInternalServerError)
		log.Printf("Failed to check if code has been used: %v", err)
		return
	}

	var itemsSold int
	err = tx.QueryRowContext(ctx, "SELECT items_sold FROM flash_sales WHERE id = $1", saleID).Scan(&itemsSold)
	if err != nil {
		http.Error(w, "Failed to check sale status", http.StatusInternalServerError)
		log.Printf("Failed to check sale status: %v", err)
		return
	}

	if itemsSold >= FlashSaleSize {
		http.Error(w, "Sale has reached the limit", http.StatusGone)
		return
	}

	userTotalItems, err := a.getUserTotalItems(ctx, userID, saleID)
	if err != nil {
		http.Error(w, "Failed to check user items", http.StatusInternalServerError)
		log.Printf("Failed to check user items: %v", err)
		return
	}

	if userTotalItems >= MaxItemsPerUser {
		http.Error(w, fmt.Sprintf("User has reached the maximum of %d items per sale", MaxItemsPerUser), http.StatusTooManyRequests)
		return
	}

	var exists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM items WHERE id = $1 AND sale_id = $2)", itemID, saleID).Scan(&exists)
	if err != nil {
		http.Error(w, "Failed to check if item exists", http.StatusInternalServerError)
		log.Printf("Failed to check if item exists: %v", err)
		return
	}

	if !exists {
		http.Error(w, "Item does not exist", http.StatusNotFound)
		log.Printf("Item with ID %d for sale %d does not exist", itemID, saleID)
		return
	}

	_, err = tx.ExecContext(ctx, "UPDATE items SET sold = TRUE, owner_id = $3 WHERE id = $1 AND sale_id = $2", itemID, saleID, userID)
	if err != nil {
		http.Error(w, "Failed to mark item as sold", http.StatusInternalServerError)
		log.Printf("Failed to mark item as sold: %v", err)
		return
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO purchases (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
		userID, itemID, code, time.Now().UTC(), saleID)
	if err != nil {
		http.Error(w, "Failed to record purchase", http.StatusInternalServerError)
		log.Printf("Failed to record purchase: %v", err)
		return
	}

	_, err = tx.ExecContext(ctx, "UPDATE flash_sales SET items_sold = items_sold + 1 WHERE id = $1", saleID)
	if err != nil {
		http.Error(w, "Failed to update sale", http.StatusInternalServerError)
		log.Printf("Failed to update sale: %v", err)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Failed to commit transaction", http.StatusInternalServerError)
		log.Printf("Failed to commit transaction: %v", err)
		return
	}

	if err := a.rdb.Del(ctx, "code:"+code).Err(); err != nil {
		log.Printf("Failed to delete code from Redis: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Purchase successful",
	})
}

func (a *App) getCurrentSale() (*FlashSale, error) {
	now := time.Now().UTC()
	startTime := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.UTC)

	if now.After(startTime.Add(time.Hour)) {
		startTime = startTime.Add(time.Hour)
	}

	var sale FlashSale
	err := a.db.QueryRow("SELECT id, start_time, items_sold FROM flash_sales WHERE start_time = $1", startTime).
		Scan(&sale.ID, &sale.StartTime, &sale.ItemsSold)

	if errors.Is(err, sql.ErrNoRows) {
		a.saleMutex.Lock()
		defer a.saleMutex.Unlock()

		err := a.db.QueryRow("SELECT id, start_time, items_sold FROM flash_sales WHERE start_time = $1", startTime).
			Scan(&sale.ID, &sale.StartTime, &sale.ItemsSold)

		if errors.Is(err, sql.ErrNoRows) {
			err = a.db.QueryRow("INSERT INTO flash_sales (start_time, items_sold) VALUES ($1, 0) RETURNING id, start_time, items_sold",
				startTime).Scan(&sale.ID, &sale.StartTime, &sale.ItemsSold)
			if err != nil {
				return nil, fmt.Errorf("failed to create new sale: %w", err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("failed to query current sale: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to query current sale: %w", err)
	}

	return &sale, nil
}

func (a *App) generateItems(saleID int) error {
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO items (name, image, sale_id, sold) VALUES ($1, $2, $3, FALSE) RETURNING id")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	categories := []string{
		"Electronics", "Clothing", "Home", "Kitchen", "Sports",
		"Toys", "Books", "Beauty", "Jewelry", "Automotive",
	}

	adjectives := []string{
		"Premium", "Deluxe", "Essential", "Classic", "Modern",
		"Vintage", "Luxury", "Budget", "Professional", "Compact",
	}

	ctx := context.Background()

	for i := 0; i < FlashSaleSize; i++ {
		categoryIndex := rand.Intn(len(categories))
		adjectiveIndex := rand.Intn(len(adjectives))

		name := fmt.Sprintf("%s %s #%d",
			adjectives[adjectiveIndex],
			categories[categoryIndex],
			i+1)

		randomParam := rand.Intn(10000)
		image := fmt.Sprintf("https://picsum.photos/200/300?random=%d-%d-%d",
			saleID, i+1, randomParam)

		var itemID int
		err := stmt.QueryRow(name, image, saleID).Scan(&itemID)
		if err != nil {
			return fmt.Errorf("failed to insert item and get ID: %w", err)
		}

		itemKey := fmt.Sprintf("item:%d:%d", saleID, itemID)
		err = a.rdb.Set(ctx, itemKey, "1", time.Hour).Err()
		if err != nil {
			log.Printf("Failed to store item in Redis: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (a *App) getUserTotalItems(ctx context.Context, userID string, saleID int) (int, error) {
	var purchaseCount int
	err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM purchases WHERE user_id = $1 AND sale_id = $2", userID, saleID).Scan(&purchaseCount)
	if err != nil {
		return 0, fmt.Errorf("failed to count user purchases: %w", err)
	}

	return purchaseCount, nil
}

func (a *App) isItemAvailable(ctx context.Context, itemID, saleID int) (bool, error) {
	var itemsSold int
	err := a.db.QueryRowContext(ctx, "SELECT items_sold FROM flash_sales WHERE id = $1", saleID).Scan(&itemsSold)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("sale with ID %d does not exist", saleID)
	} else if err != nil {
		return false, fmt.Errorf("failed to check items sold: %w", err)
	}

	if itemsSold >= FlashSaleSize {
		return false, nil
	}

	var itemExists bool
	var sold bool

	itemKey := fmt.Sprintf("item:%d:%d", saleID, itemID)
	exists, err := a.rdb.Exists(ctx, itemKey).Result()
	if err != nil {
		log.Printf("Failed to check if item exists in Redis: %v", err)
		err = a.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM items WHERE id = $1 AND sale_id = $2), sold FROM items WHERE id = $1 AND sale_id = $2", itemID, saleID).Scan(&itemExists, &sold)
		if err != nil {
			return false, fmt.Errorf("failed to check if item exists: %w", err)
		}
	} else if exists == 1 {
		err = a.db.QueryRowContext(ctx, "SELECT sold FROM items WHERE id = $1 AND sale_id = $2", itemID, saleID).Scan(&sold)
		if err != nil {
			return false, fmt.Errorf("failed to check if item is sold: %w", err)
		}
		itemExists = true
	} else {
		err = a.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM items WHERE id = $1 AND sale_id = $2), sold FROM items WHERE id = $1 AND sale_id = $2", itemID, saleID).Scan(&itemExists, &sold)
		if err != nil {
			return false, fmt.Errorf("failed to check if item exists: %w", err)
		}

		if itemExists {
			err = a.rdb.Set(ctx, itemKey, "1", time.Hour).Err()
			if err != nil {
				log.Printf("Failed to store item in Redis: %v", err)
			}
		}
	}

	if !itemExists {
		return false, fmt.Errorf("item with ID %d does not exist", itemID)
	}

	if sold {
		return false, fmt.Errorf("item with ID %d is already sold", itemID)
	}

	return true, nil
}

func (a *App) storeCheckoutAttempt(ctx context.Context, userID string, itemID int, code string, saleID int, status string) error {
	_, err := a.db.ExecContext(ctx, "INSERT INTO checkout_attempts (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
		userID, itemID, code, time.Now().UTC(), saleID)
	if err != nil {
		return fmt.Errorf("failed to store checkout attempt: %w", err)
	}
	return nil
}

func generateUniqueCode() (string, error) {
	b := make([]byte, 16)
	_, err := cryptorand.Read(b)
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(b), nil
}

func (a *App) startNewSale() error {
	log.Println("Starting a new flash sale...")
	sale, err := a.getCurrentSale()
	if err != nil {
		return fmt.Errorf("failed to start new sale: %w", err)
	}

	var count int
	err = a.db.QueryRow("SELECT COUNT(*) FROM items WHERE sale_id = $1", sale.ID).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check if items exist for sale: %w", err)
	}

	if count > 0 {
		log.Printf("Items already exist for sale %d, skipping item generation", sale.ID)
		return nil
	}

	if err := a.generateItems(sale.ID); err != nil {
		return fmt.Errorf("failed to generate items for sale: %w", err)
	}

	log.Println("New flash sale started successfully with all items generated")
	return nil
}

func (a *App) Run() error {
	server := &http.Server{
		Addr:    ":8080",
		Handler: a.router,
	}

	if err := a.startNewSale(); err != nil {
		log.Printf("Warning: failed to start initial sale: %v", err)
	}

	go a.startHourlySales()

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Server listening on %s", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)
	case <-shutdown:
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			if err := server.Close(); err != nil {
				return fmt.Errorf("could not stop server gracefully: %w", err)
			}
		}
	}

	return nil
}

func (a *App) startHourlySales() {
	now := time.Now().UTC()
	nextHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, time.UTC)
	initialDelay := nextHour.Sub(now)

	time.Sleep(initialDelay)

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := a.startNewSale(); err != nil {
				log.Printf("Error starting new sale: %v", err)
			}
		}
	}
}

func (a *App) Close() error {
	if err := a.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	if err := a.rdb.Close(); err != nil {
		return fmt.Errorf("failed to close Redis: %w", err)
	}

	return nil
}

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatalf("Failed to create app: %v", err)
	}
	defer app.Close()

	if err := app.Run(); err != nil {
		log.Fatalf("Failed to run app: %v", err)
	}
}
