package server

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"not-sale-back/internal/config"
	"not-sale-back/internal/database"
	"not-sale-back/internal/handlers"
	"not-sale-back/internal/models"
	"not-sale-back/internal/redis"
)

type Server struct {
	db      *database.Database
	rdb     *redis.RedisClient
	router  *chi.Mux
	handler *handlers.Handler
	config  *config.Config
}

func NewServer(cfg *config.Config) (*Server, error) {
	db, err := database.NewDatabase(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create database: %w", err)
	}

	if err := db.InitDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	rdb, err := redis.NewRedisClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Redis client: %w", err)
	}

	r := chi.NewRouter()

	if cfg.EnableRequestLogger {
		r.Use(middleware.Logger)
	}

	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(cfg.RequestTimeout))
	r.Use(middleware.ThrottleBacklog(cfg.MaxConcurrentReqs, 1000, time.Second*60))
	r.Use(middleware.Heartbeat("/health"))
	r.Use(middleware.RequestID)

	handler := handlers.NewHandler(db, rdb)

	server := &Server{
		db:      db,
		rdb:     rdb,
		router:  r,
		handler: handler,
		config:  cfg,
	}

	server.setupRoutes()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.populateItemsCache(ctx); err != nil {
		log.Printf("Warning: failed to populate items cache: %v", err)
	}

	return server, nil
}

func (s *Server) setupRoutes() {
	s.router.Post("/checkout", s.handler.HandleCheckout)
	s.router.Post("/purchase", s.handler.HandlePurchase)
}

func (s *Server) populateItemsCache(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var items []struct {
		ID     int
		SaleID int
	}

	err := s.db.ExecuteWithRetry(ctx, func() error {
		var err error
		items, err = s.db.GetAllItems()
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to get items: %w", err)
	}

	return s.rdb.ExecuteWithRetry(ctx, func(ctx context.Context) error {
		return s.rdb.PopulateItemsCache(ctx, items, time.Hour)
	})
}

func (s *Server) startNewSale() error {
	log.Println("Starting a new flash sale...")
	sale, err := s.db.GetCurrentSale()
	if err != nil {
		return fmt.Errorf("failed to start new sale: %w", err)
	}

	count, err := s.db.GetItemsForSale(sale.ID)
	if err != nil {
		return fmt.Errorf("failed to check if items exist for sale: %w", err)
	}

	if count > 0 {
		log.Printf("Items already exist for sale %d, skipping item generation", sale.ID)
		return nil
	}

	if err := s.generateItems(sale.ID); err != nil {
		return fmt.Errorf("failed to generate items for sale: %w", err)
	}

	log.Println("New flash sale started successfully with all items generated")
	return nil
}

func (s *Server) generateItems(saleID int) error {
	categories := []string{
		"Electronics", "Clothing", "Home", "Kitchen", "Sports",
		"Toys", "Books", "Beauty", "Jewelry", "Automotive",
		"Furniture", "Garden", "Outdoor", "Fitness", "Baby",
		"Pet Supplies", "Office", "Art", "Music", "Gaming",
		"Travel", "Health", "Food", "Stationery", "Accessories",
		"Footwear", "Watches", "Appliances", "DIY", "Photography",
		"Camping", "Crafts", "Party", "Seasonal", "Tech Gadgets",
		"Smartphones", "Computers", "Audio", "Lighting", "Bedding",
	}

	adjectives := []string{
		"Premium", "Deluxe", "Essential", "Classic", "Modern",
		"Vintage", "Luxury", "Budget", "Professional", "Compact",
		"Elegant", "Stylish", "Durable", "Portable", "Advanced",
		"Smart", "Eco-Friendly", "Handcrafted", "Innovative", "Sleek",
		"Ergonomic", "Customizable", "Exclusive", "Practical", "Trendy",
		"Minimalist", "Colorful", "Multifunctional", "Lightweight", "Waterproof",
		"Wireless", "Organic", "Adjustable", "Foldable", "High-Performance",
		"Limited Edition", "Rechargeable", "Retro", "Signature", "Ultra-Thin",
	}

	itemNames := make([]string, models.FlashSaleSize)
	itemImages := make([]string, models.FlashSaleSize)

	for i := 0; i < models.FlashSaleSize; i++ {
		categoryIndex := rand.Intn(len(categories))
		adjectiveIndex := rand.Intn(len(adjectives))

		itemNames[i] = fmt.Sprintf("%s %s #%d",
			adjectives[adjectiveIndex],
			categories[categoryIndex],
			i+1)

		randomParam := rand.Intn(10000)
		itemImages[i] = fmt.Sprintf("https://picsum.photos/200/300?random=%d-%d-%d",
			saleID, i+1, randomParam)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := s.db.GenerateItems(ctx, saleID, itemNames, itemImages)
	if err != nil {
		return fmt.Errorf("failed to generate items: %w", err)
	}

	items, err := s.db.GetAllItems()
	if err != nil {
		log.Printf("Warning: failed to get items for caching: %v", err)
		return nil
	}

	for _, item := range items {
		if item.SaleID == saleID {
			itemKey := fmt.Sprintf("item:%d:%d", saleID, item.ID)
			err = s.rdb.GetClient().Set(ctx, itemKey, "1", time.Hour).Err()
			if err != nil {
				log.Printf("Failed to store item in Redis: %v", err)
			}
		}
	}

	return nil
}

func (s *Server) startHourlySales() {
	now := time.Now().UTC()
	nextHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, time.UTC)
	initialDelay := nextHour.Sub(now)

	time.Sleep(initialDelay)

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.startNewSale(); err != nil {
				log.Printf("Error starting new sale: %v", err)
			}
		}
	}
}

func (s *Server) Run() error {
	server := &http.Server{
		Addr:         ":" + s.config.Port,
		Handler:      s.router,
		ReadTimeout:  s.config.ServerReadTimeout,
		WriteTimeout: s.config.ServerWriteTimeout,
		IdleTimeout:  s.config.ServerIdleTimeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.startNewSaleWithRetry(ctx); err != nil {
		log.Printf("Warning: failed to start initial sale: %v", err)
	}

	go s.startHourlySales()

	serverErrors := make(chan error, 1)

	go func() {
		log.Printf("Server listening on %s", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)
	case sig := <-shutdown:
		log.Printf("Received signal %v, initiating graceful shutdown...", sig)

		ctx, cancel := context.WithTimeout(context.Background(), s.config.ServerShutdownTimeout)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown failed: %v, forcing immediate shutdown", err)
			if err := server.Close(); err != nil {
				return fmt.Errorf("could not stop server gracefully or immediately: %w", err)
			}
		}

		log.Println("Server shutdown complete")
	}

	return nil
}

func (s *Server) startNewSaleWithRetry(ctx context.Context) error {
	var lastErr error
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := s.startNewSale()
		if err == nil {
			return nil
		}

		lastErr = err
		log.Printf("Failed to start new sale, retrying (%d/%d): %v", i+1, maxRetries, err)

		retryTime := time.Duration(1<<uint(i)) * 500 * time.Millisecond

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryTime):
			// Continue
		}
	}

	return fmt.Errorf("failed to start new sale after %d retries: %w", maxRetries, lastErr)
}

func (s *Server) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	if err := s.rdb.Close(); err != nil {
		return fmt.Errorf("failed to close Redis: %w", err)
	}

	return nil
}
