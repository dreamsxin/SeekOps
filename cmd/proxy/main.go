package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"deepseek-proxy/internal/proxy"
)

func main() {
	sqlitePath := envOr("SQLITE_PATH", "data/seekops.db")
	db, err := proxy.OpenSQLite(sqlitePath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var secrets *proxy.SecretCipher
	if masterKey := os.Getenv("SECRETS_MASTER_KEY"); masterKey != "" {
		secrets, err = proxy.NewSecretCipherFromBase64(masterKey)
	} else {
		keyPath := envOr("SECRETS_MASTER_KEY_FILE", proxy.DefaultSecretKeyPath(sqlitePath))
		secrets, err = proxy.OpenSecretCipher(db, keyPath)
	}
	if err != nil {
		log.Fatalf("open secrets master key: %v", err)
	}
	cfg := proxy.Config{
		ListenAddr:             os.Getenv("LISTEN_ADDR"),
		PublicBaseURL:          os.Getenv("PUBLIC_BASE_URL"),
		PlatformAPIKey:         envOr("PLATFORM_API_KEY", "proxy-demo-key"),
		AdminAPIKey:            envOr("ADMIN_API_KEY", envOr("PLATFORM_API_KEY", "proxy-demo-key")),
		RequestTimeout:         durationOr("REQUEST_TIMEOUT", 10*time.Minute),
		SessionAffinityTTL:     durationOr("SESSION_AFFINITY_TTL", 24*time.Hour),
		SessionAffinityMax:     intEnv("SESSION_AFFINITY_MAX_ENTRIES", 100000, 1, 1000000),
		SessionAffinityPercent: intEnv("SESSION_AFFINITY_PERCENT", 90, 0, 100),
		Accounts:               loadAccounts(),
		DB:                     db,
		SecretCipher:           secrets,
		PriceInputHit:          floatEnv("PRICE_INPUT_HIT_CNY_PER_MILLION", 0.02),
		PriceInputMiss:         floatEnv("PRICE_INPUT_MISS_CNY_PER_MILLION", 1),
		PriceOutput:            floatEnv("PRICE_OUTPUT_CNY_PER_MILLION", 2),
	}
	server, err := proxy.NewServerChecked(cfg)
	if err != nil {
		log.Fatalf("initialize proxy: %v", err)
	}
	if server.AccountCount() == 0 {
		log.Print("warning: no upstream account configured; /readyz will fail")
	}
	go server.StartBalancePoller(context.Background(), durationOr("BALANCE_POLL_INTERVAL", 5*time.Minute))
	log.Fatal(server.ListenAndServe())
}

func loadAccounts() []*proxy.Account {
	if raw := os.Getenv("UPSTREAM_ACCOUNTS_JSON"); raw != "" {
		type accountInput struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			APIKey  string   `json:"api_key"`
			BaseURL string   `json:"base_url"`
			Weight  int      `json:"weight"`
			Models  []string `json:"models"`
		}
		var inputs []accountInput
		if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
			log.Fatalf("invalid UPSTREAM_ACCOUNTS_JSON: %v", err)
		}
		accounts := make([]*proxy.Account, 0, len(inputs))
		for i, input := range inputs {
			account := &proxy.Account{ID: input.ID, Name: input.Name, APIKey: input.APIKey, BaseURL: input.BaseURL, Weight: input.Weight, Models: input.Models}
			normalizeAccount(account, i)
			accounts = append(accounts, account)
		}
		return accounts
	}
	keys := strings.Split(os.Getenv("UPSTREAM_API_KEYS"), ",")
	if len(keys) == 1 && strings.TrimSpace(keys[0]) == "" {
		keys = []string{os.Getenv("UPSTREAM_API_KEY")}
	}
	accounts := make([]*proxy.Account, 0, len(keys))
	for i, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		account := &proxy.Account{ID: "acct-" + strconv.Itoa(i+1), Name: "DeepSeek account " + strconv.Itoa(i+1), APIKey: key}
		normalizeAccount(account, i)
		accounts = append(accounts, account)
	}
	return accounts
}
func normalizeAccount(account *proxy.Account, index int) {
	if account.ID == "" {
		account.ID = "acct-" + strconv.Itoa(index+1)
	}
	if account.Name == "" {
		account.Name = account.ID
	}
	if account.BaseURL == "" {
		account.BaseURL = "https://api.deepseek.com"
	}
	if account.Weight <= 0 {
		account.Weight = 1
	}
}
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func durationOr(name string, fallback time.Duration) time.Duration {
	if value := os.Getenv(name); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
		log.Printf("invalid %s=%q; using %s", name, value, fallback)
	}
	return fallback
}

func floatEnv(name string, fallback float64) float64 {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed >= 0 {
			return parsed
		}
		log.Printf("invalid %s=%q; using %.8f", name, value, fallback)
	}
	return fallback
}

func intEnv(name string, fallback, minimum, maximum int) int {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= minimum && parsed <= maximum {
			return parsed
		}
		log.Printf("invalid %s=%q; using %d", name, value, fallback)
	}
	return fallback
}
