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
	db, err := database.NewDatabase(cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create database: %w", err)
	}

	if err := db.InitDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	rdb, err := redis.NewRedisClient(cfg.RedisAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to create Redis client: %w", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	handler := handlers.NewHandler(db, rdb)

	server := &Server{
		db:      db,
		rdb:     rdb,
		router:  r,
		handler: handler,
		config:  cfg,
	}

	server.setupRoutes()

	if err := server.populateItemsCache(); err != nil {
		log.Printf("Warning: failed to populate items cache: %v", err)
	}

	return server, nil
}

func (s *Server) setupRoutes() {
	s.router.Post("/checkout", s.handler.HandleCheckout)
	s.router.Post("/purchase", s.handler.HandlePurchase)
}

func (s *Server) populateItemsCache() error {
	items, err := s.db.GetAllItems()
	if err != nil {
		return fmt.Errorf("failed to get items: %w", err)
	}

	ctx := context.Background()
	return s.rdb.PopulateItemsCache(ctx, items, time.Hour)
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
	tx, err := s.db.GetDB().Begin()
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

	for i := 0; i < models.FlashSaleSize; i++ {
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
		err = s.rdb.GetClient().Set(ctx, itemKey, "1", time.Hour).Err()
		if err != nil {
			log.Printf("Failed to store item in Redis: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
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
		Addr:    ":" + s.config.Port,
		Handler: s.router,
	}

	if err := s.startNewSale(); err != nil {
		log.Printf("Warning: failed to start initial sale: %v", err)
	}

	go s.startHourlySales()

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

func (s *Server) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	if err := s.rdb.Close(); err != nil {
		return fmt.Errorf("failed to close Redis: %w", err)
	}

	return nil
}
