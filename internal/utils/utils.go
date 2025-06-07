package utils

import (
	"crypto/rand"
	"encoding/base64"
)

func GenerateUniqueCode() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(b), nil
}
