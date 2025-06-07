package models

import (
	"time"
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
