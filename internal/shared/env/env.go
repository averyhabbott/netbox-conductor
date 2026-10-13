// Package env reads configuration values from environment variables.
package env

import (
	"log"
	"os"
	"strconv"
)

// Or returns the value of key, or fallback when it is unset or empty.
func Or(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Int returns key parsed as a positive integer, or fallback when it is unset,
// not a number, or not positive.
func Int(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// Require returns the value of key and exits the process when it is unset or empty.
func Require(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}
