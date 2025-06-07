package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"not-sale-back/internal/models"
	"not-sale-back/internal/utils"
)

type DatabaseService interface {
	GetUserTotalItems(ctx context.Context, userID string, saleID int) (int, error)
	StoreCheckoutAttempt(ctx context.Context, userID string, itemID int, code string, saleID int, status string) error
	GetDB() *sql.DB
}

type RedisService interface {
	StoreCode(ctx context.Context, code, data string, expiration time.Duration) error
	GetCode(ctx context.Context, code string) (string, error)
	DeleteCode(ctx context.Context, code string) error
	ItemExists(ctx context.Context, saleID, itemID int) (bool, error)
	GetClient() *redis.Client
}

type Handler struct {
	db    DatabaseService
	redis RedisService
}

func NewHandler(db DatabaseService, redis RedisService) *Handler {
	return &Handler{
		db:    db,
		redis: redis,
	}
}

func (h *Handler) HandleCheckout(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	itemIDStr := r.URL.Query().Get("id")
	ctx := r.Context()

	code, err := utils.GenerateUniqueCode()
	if err != nil {
		http.Error(w, "Failed to generate code", http.StatusInternalServerError)
		log.Printf("Failed to generate code: %v", err)
		return
	}

	if userID == "" || itemIDStr == "" {
		if err := h.db.StoreCheckoutAttempt(ctx, userID, 0, code, 0, "error_missing_params"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Missing user_id or id parameter", http.StatusBadRequest)
		return
	}

	itemID, err := strconv.Atoi(itemIDStr)
	if err != nil {
		if err := h.db.StoreCheckoutAttempt(ctx, userID, 0, code, 0, "error_invalid_item_id"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Invalid item ID", http.StatusBadRequest)
		return
	}

	db := h.db.GetDB()
	rows, err := db.QueryContext(ctx, "SELECT id, start_time FROM flash_sales WHERE items_sold < $1 ORDER BY start_time DESC", models.FlashSaleSize)
	if err != nil {
		if err := h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_query_sales"); err != nil {
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
			if err := h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_scan_sale_id"); err != nil {
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

		available, err := h.redis.ItemExists(ctx, saleID, itemID)
		if err == nil && available {
			itemFound = true
			foundSaleID = saleID
			break
		}
	}

	if !itemFound {
		if err := h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_item_not_found"); err != nil {
			log.Printf("Failed to store checkout attempt: %v", err)
		}
		http.Error(w, "Item not found in any active sale", http.StatusNotFound)
		return
	}

	saleID = foundSaleID

	if err := h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, saleID, "success"); err != nil {
		http.Error(w, "Failed to store checkout attempt", http.StatusInternalServerError)
		log.Printf("Failed to store checkout attempt: %v", err)
		return
	}

	if err := h.redis.StoreCode(ctx, code, fmt.Sprintf("%s:%d:%d", userID, itemID, saleID), models.CodeExpirationTime); err != nil {
		http.Error(w, "Failed to store code", http.StatusInternalServerError)
		log.Printf("Failed to store code in Redis: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func (h *Handler) HandlePurchase(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")

	if code == "" {
		http.Error(w, "Missing code parameter", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	codeData, err := h.redis.GetCode(ctx, code)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			http.Error(w, "Invalid or expired code", http.StatusBadRequest)
		} else {
			http.Error(w, "Failed to verify code", http.StatusInternalServerError)
			log.Printf("Failed to get code from Redis: %v", err)
		}
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

	db := h.db.GetDB()
	tx, err := db.BeginTx(ctx, nil)
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
	} else if !errors.Is(err, sql.ErrNoRows) {
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

	if itemsSold >= models.FlashSaleSize {
		http.Error(w, "Sale has reached the limit", http.StatusGone)
		return
	}

	userTotalItems, err := h.db.GetUserTotalItems(ctx, userID, saleID)
	if err != nil {
		http.Error(w, "Failed to check user items", http.StatusInternalServerError)
		log.Printf("Failed to check user items: %v", err)
		return
	}

	if userTotalItems >= models.MaxItemsPerUser {
		http.Error(w, fmt.Sprintf("User has reached the maximum of %d items per sale", models.MaxItemsPerUser), http.StatusTooManyRequests)
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

	if err := h.redis.DeleteCode(ctx, code); err != nil {
		log.Printf("Failed to delete code from Redis: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Purchase successful",
	})
}
