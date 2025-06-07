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
	ExecuteWithRetry(ctx context.Context, operation func() error) error
}

type RedisService interface {
	StoreCode(ctx context.Context, code, data string, expiration time.Duration) error
	GetCode(ctx context.Context, code string) (string, error)
	DeleteCode(ctx context.Context, code string) error
	ItemExists(ctx context.Context, saleID, itemID int) (bool, error)
	GetClient() *redis.Client
	ExecuteWithRetry(ctx context.Context, operation func(context.Context) error) error
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var code string
	err := h.db.ExecuteWithRetry(ctx, func() error {
		var err error
		code, err = utils.GenerateUniqueCode()
		return err
	})

	if err != nil {
		http.Error(w, "Failed to generate code", http.StatusInternalServerError)
		log.Printf("Failed to generate code after retries: %v", err)
		return
	}

	if userID == "" || itemIDStr == "" {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, 0, code, 0, "error_missing_params")
		http.Error(w, "Missing user_id or id parameter", http.StatusBadRequest)
		return
	}

	itemID, err := strconv.Atoi(itemIDStr)
	if err != nil {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, 0, code, 0, "error_invalid_item_id")
		http.Error(w, "Invalid item ID", http.StatusBadRequest)
		return
	}

	var saleID int
	var itemFound bool

	err = h.db.ExecuteWithRetry(ctx, func() error {
		db := h.db.GetDB()
		rows, err := db.QueryContext(ctx, "SELECT id, start_time FROM flash_sales WHERE items_sold < $1 ORDER BY start_time DESC", models.FlashSaleSize)
		if err != nil {
			return fmt.Errorf("failed to query sales: %w", err)
		}
		defer rows.Close()

		now := time.Now().UTC()
		for rows.Next() {
			var currentSaleID int
			var startTime time.Time

			if err := rows.Scan(&currentSaleID, &startTime); err != nil {
				return fmt.Errorf("failed to scan sale ID: %w", err)
			}

			if now.Before(startTime) {
				continue
			}

			var available bool
			err = h.redis.ExecuteWithRetry(ctx, func(rctx context.Context) error {
				var err error
				available, err = h.redis.ItemExists(rctx, currentSaleID, itemID)
				return err
			})

			if err == nil && available {
				itemFound = true
				saleID = currentSaleID
				return nil
			}
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating through sales: %w", err)
		}

		return nil
	})

	if err != nil {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_query_sales")
		http.Error(w, "Failed to query sales", http.StatusInternalServerError)
		log.Printf("Failed to query sales after retries: %v", err)
		return
	}

	if !itemFound {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_item_not_found")
		http.Error(w, "Item not found in any active sale", http.StatusNotFound)
		return
	}

	err = h.db.ExecuteWithRetry(ctx, func() error {
		return h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, saleID, "success")
	})

	if err != nil {
		http.Error(w, "Failed to store checkout attempt", http.StatusInternalServerError)
		log.Printf("Failed to store checkout attempt after retries: %v", err)
		return
	}

	err = h.redis.ExecuteWithRetry(ctx, func(rctx context.Context) error {
		return h.redis.StoreCode(rctx, code, fmt.Sprintf("%s:%d:%d", userID, itemID, saleID), models.CodeExpirationTime)
	})

	if err != nil {
		http.Error(w, "Failed to store code", http.StatusInternalServerError)
		log.Printf("Failed to store code in Redis after retries: %v", err)
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var codeData string
	err := h.redis.ExecuteWithRetry(ctx, func(rctx context.Context) error {
		var err error
		codeData, err = h.redis.GetCode(rctx, code)
		return err
	})

	if err != nil {
		if strings.Contains(err.Error(), "code not found or expired") {
			http.Error(w, "Invalid or expired code", http.StatusBadRequest)
		} else {
			http.Error(w, "Failed to verify code", http.StatusInternalServerError)
			log.Printf("Failed to get code from Redis after retries: %v", err)
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

	var purchaseSuccess bool

	err = h.db.ExecuteWithRetry(ctx, func() error {
		db := h.db.GetDB()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{
			Isolation: sql.LevelReadCommitted,
		})
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		var purchaseID int
		err = tx.QueryRowContext(ctx, "SELECT id FROM purchases WHERE code = $1", code).Scan(&purchaseID)
		if err == nil {
			return fmt.Errorf("code has already been used")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to check if code has been used: %w", err)
		}

		var itemsSold int
		err = tx.QueryRowContext(ctx, "SELECT items_sold FROM flash_sales WHERE id = $1", saleID).Scan(&itemsSold)
		if err != nil {
			return fmt.Errorf("failed to check sale status: %w", err)
		}

		if itemsSold >= models.FlashSaleSize {
			return fmt.Errorf("sale has reached the limit")
		}

		userTotalItems, err := h.db.GetUserTotalItems(ctx, userID, saleID)
		if err != nil {
			return fmt.Errorf("failed to check user items: %w", err)
		}

		if userTotalItems >= models.MaxItemsPerUser {
			return fmt.Errorf("user has reached the maximum of %d items per sale", models.MaxItemsPerUser)
		}

		var exists bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM items WHERE id = $1 AND sale_id = $2 AND sold = FALSE)",
			itemID, saleID).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check if item exists: %w", err)
		}

		if !exists {
			return fmt.Errorf("item does not exist or is already sold")
		}

		result, err := tx.ExecContext(ctx, "UPDATE items SET sold = TRUE, owner_id = $3 WHERE id = $1 AND sale_id = $2 AND sold = FALSE",
			itemID, saleID, userID)
		if err != nil {
			return fmt.Errorf("failed to mark item as sold: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		}

		if rowsAffected == 0 {
			return fmt.Errorf("item was already sold")
		}

		_, err = tx.ExecContext(ctx, "INSERT INTO purchases (user_id, item_id, code, created_at, sale_id) VALUES ($1, $2, $3, $4, $5)",
			userID, itemID, code, time.Now().UTC(), saleID)
		if err != nil {
			return fmt.Errorf("failed to record purchase: %w", err)
		}

		_, err = tx.ExecContext(ctx, "UPDATE flash_sales SET items_sold = items_sold + 1 WHERE id = $1", saleID)
		if err != nil {
			return fmt.Errorf("failed to update sale: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		purchaseSuccess = true
		return nil
	})

	if err != nil {
		if strings.Contains(err.Error(), "code has already been used") {
			http.Error(w, "Code has already been used", http.StatusConflict)
		} else if strings.Contains(err.Error(), "sale has reached the limit") {
			http.Error(w, "Sale has reached the limit", http.StatusGone)
		} else if strings.Contains(err.Error(), "user has reached the maximum") {
			http.Error(w, fmt.Sprintf("User has reached the maximum of %d items per sale", models.MaxItemsPerUser), http.StatusTooManyRequests)
		} else if strings.Contains(err.Error(), "item does not exist") || strings.Contains(err.Error(), "item was already sold") {
			http.Error(w, "Item does not exist or is already sold", http.StatusNotFound)
			log.Printf("Item with ID %d for sale %d does not exist or is already sold", itemID, saleID)
		} else {
			http.Error(w, "Failed to process purchase", http.StatusInternalServerError)
			log.Printf("Failed to process purchase after retries: %v", err)
		}
		return
	}

	if purchaseSuccess {
		err = h.redis.ExecuteWithRetry(ctx, func(rctx context.Context) error {
			return h.redis.DeleteCode(rctx, code)
		})

		if err != nil {
			log.Printf("Failed to delete code from Redis after retries: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Purchase successful",
	})
}
