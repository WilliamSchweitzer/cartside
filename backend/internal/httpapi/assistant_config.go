package httpapi

import (
	"errors"
	"strconv"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/shoppingassistant"
)

// ConfigureAssistant does not inspect AI credentials or construct a client when disabled.
func ConfigureAssistant(getenv func(string) string, catalog shoppingassistant.Catalog) (Assistant, *AssistantLimiter, error) {
	flag := getenv("CARTSIDE_ASSISTANT_ENABLED")
	if flag == "" || flag == "false" {
		return nil, nil, nil
	}
	if flag != "true" {
		return nil, nil, errors.New("invalid CARTSIDE_ASSISTANT_ENABLED")
	}
	limit, capacity := 10, 1000
	for name, target := range map[string]*int{"CARTSIDE_ASSISTANT_REQUESTS_PER_MINUTE": &limit, "CARTSIDE_ASSISTANT_MAX_USERS": &capacity} {
		if raw := getenv(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				return nil, nil, errors.New("invalid assistant limit configuration")
			}
			*target = n
		}
	}
	client, err := shoppingassistant.NewClient(getenv("CARTSIDE_AI_GATEWAY_URL"), getenv("CARTSIDE_AI_SERVICE_KEY"))
	if err != nil {
		return nil, nil, errors.New("invalid assistant gateway configuration")
	}
	limiter, err := NewAssistantLimiter(limit, capacity, time.Minute, time.Now)
	return shoppingassistant.New(catalog, client), limiter, err
}
