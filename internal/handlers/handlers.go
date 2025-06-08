package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"not-sale-back/internal/models"
	"not-sale-back/internal/utils"
)

type DatabaseService interface {
	GetUserTotalItems(ctx context.Context, userID string, saleID int) (int, error)
	StoreCheckoutAttempt(ctx context.Context, userID string, itemID int, code string, saleID int, status string) error
	ExecuteWithRetry(ctx context.Context, operation func() error) error
	GetSaleIDByItemID(ctx context.Context, itemID int) (int, bool, error)

	GetActiveSales(ctx context.Context, limit int) ([]struct {
		ID        int
		StartTime time.Time
	}, error)
	BeginTx(ctx context.Context) (pgx.Tx, error)
	CheckCodeUsed(ctx context.Context, tx pgx.Tx, code string) (bool, int, error)
	GetSaleItemsSold(ctx context.Context, tx pgx.Tx, saleID int) (int, error)
	CheckItemAvailable(ctx context.Context, tx pgx.Tx, itemID, saleID int) (bool, error)
	MarkItemAsSold(ctx context.Context, tx pgx.Tx, itemID, saleID int, userID string) (int64, error)
	RecordPurchase(ctx context.Context, tx pgx.Tx, userID string, itemID int, code string, saleID int) error
	UpdateSaleCounter(ctx context.Context, tx pgx.Tx, saleID int) error
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

	code, err := utils.GenerateUniqueCode()
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

	var found bool
	saleID, found, err = h.db.GetSaleIDByItemID(ctx, itemID)
	if err != nil {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_query_item")
		http.Error(w, "Failed to query item", http.StatusInternalServerError)
		log.Printf("Failed to get sale ID for item: %v", err)
		return
	}

	if !found {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_item_not_found")
		http.Error(w, "Item not found", http.StatusNotFound)
		return
	}

	var available bool
	err = h.redis.ExecuteWithRetry(ctx, func(rctx context.Context) error {
		var err error
		available, err = h.redis.ItemExists(rctx, saleID, itemID)
		return err
	})
	if err != nil {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_check_item")
		http.Error(w, "Failed to check item availability", http.StatusInternalServerError)
		log.Printf("Failed to check if item exists in Redis: %v", err)
		return
	}

	if !available {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_item_not_available")
		http.Error(w, "Item not available", http.StatusNotFound)
		return
	}

	var itemAvailable bool
	err = h.db.ExecuteWithRetry(ctx, func() error {
		tx, err := h.db.BeginTx(ctx)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		var checkErr error
		itemAvailable, checkErr = h.db.CheckItemAvailable(ctx, tx, itemID, saleID)
		return checkErr
	})

	if err != nil {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_check_item_available")
		http.Error(w, "Failed to check if item is available", http.StatusInternalServerError)
		log.Printf("Failed to check if item is available: %v", err)
		return
	}

	if !itemAvailable {
		_ = h.db.StoreCheckoutAttempt(ctx, userID, itemID, code, 0, "error_item_already_sold")
		http.Error(w, "Item is already sold", http.StatusConflict)
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
		tx, err := h.db.BeginTx(ctx)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		codeUsed, _, err := h.db.CheckCodeUsed(ctx, tx, code)
		if err != nil {
			return fmt.Errorf("failed to check if code has been used: %w", err)
		}
		if codeUsed {
			return fmt.Errorf("code has already been used")
		}

		itemsSold, err := h.db.GetSaleItemsSold(ctx, tx, saleID)
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

		exists, err := h.db.CheckItemAvailable(ctx, tx, itemID, saleID)
		if err != nil {
			return fmt.Errorf("failed to check if item exists: %w", err)
		}

		if !exists {
			return fmt.Errorf("item does not exist or is already sold")
		}

		rowsAffected, err := h.db.MarkItemAsSold(ctx, tx, itemID, saleID, userID)
		if err != nil {
			return fmt.Errorf("failed to mark item as sold: %w", err)
		}

		if rowsAffected == 0 {
			return fmt.Errorf("item was already sold")
		}

		if err := h.db.RecordPurchase(ctx, tx, userID, itemID, code, saleID); err != nil {
			return fmt.Errorf("failed to record purchase: %w", err)
		}

		if err := h.db.UpdateSaleCounter(ctx, tx, saleID); err != nil {
			return fmt.Errorf("failed to update sale: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
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

func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	status := map[string]string{
		"status":   "ok",
		"database": "ok",
		"redis":    "ok",
	}

	dbErr := h.db.ExecuteWithRetry(ctx, func() error {
		_, err := h.db.GetActiveSales(ctx, 1)
		return err
	})

	if dbErr != nil {
		status["database"] = "error"
		status["status"] = "error"
		log.Printf("Health check: Database error: %v", dbErr)
	}

	redisErr := h.redis.ExecuteWithRetry(ctx, func(rctx context.Context) error {
		return h.redis.GetClient().Ping(rctx).Err()
	})

	if redisErr != nil {
		status["redis"] = "error"
		status["status"] = "error"
		log.Printf("Health check: Redis error: %v", redisErr)
	}

	w.Header().Set("Content-Type", "application/json")
	if status["status"] != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(status)
}
