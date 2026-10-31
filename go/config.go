package isureal

import (
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Region     string
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	LogLevel   string
	// 転送先 (asia1) の URL。europe1 / america1 だけで設定する (delegate.go)
	DelegateURL string
	// 転送元として信用するサーバのIPアドレス。asia1 だけで設定する (delegate.go)
	TrustedPeers []string
	// 画像を置くディレクトリ (image_store.go)
	ImageDir string
	// 画像署名の計算を依頼するサーバの URL。asia1 だけで設定する (image_signature_remote.go)
	SigningURLs []string
	// asia1 で、直接来たリクエストを転送されたリクエストより後回しにする (priority.go)
	PriorityLimit   int
	PriorityMaxWait time.Duration
	PriorityScope   string
}

func ConfigFromEnv() *Config {
	return &Config{
		Region:     envOrDefault("ISUREAL_REGION", "asia"),
		DBHost:     envOrDefault("ISUREAL_DB_HOST", "127.0.0.1"),
		DBPort:     envOrDefault("ISUREAL_DB_PORT", "5432"),
		DBUser:     envOrDefault("ISUREAL_DB_USER", "isureal"),
		DBPassword: envOrDefault("ISUREAL_DB_PASSWORD", "isureal"),
		DBName:     envOrDefault("ISUREAL_DB_NAME", "isureal"),
		LogLevel:   envOrDefault("ISUREAL_LOG_LEVEL", "debug"),

		DelegateURL:  os.Getenv("ISUREAL_DELEGATE_URL"),
		TrustedPeers: strings.FieldsFunc(os.Getenv("ISUREAL_TRUSTED_PEERS"), func(r rune) bool { return r == ',' || r == ' ' }),
		ImageDir:     envOrDefault("ISUREAL_IMAGE_DIR", "/home/isucon/images"),
		SigningURLs:  strings.FieldsFunc(os.Getenv("ISUREAL_SIGNING_URLS"), func(r rune) bool { return r == ',' || r == ' ' }),

		PriorityLimit:   envIntOrDefault("ISUREAL_PRIORITY_LIMIT", 0),
		PriorityMaxWait: envDurationOrDefault("ISUREAL_PRIORITY_MAX_WAIT", time.Second),
		PriorityScope:   envOrDefault("ISUREAL_PRIORITY_SCOPE", "heavy"),
	}
}

func (c *Config) DatabaseDSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Host:   net.JoinHostPort(c.DBHost, c.DBPort),
		Path:   "/" + c.DBName,
		// DB とは私設ネットワーク内で通信するので TLS を使わない。
		// 指定しないと pgx は prefer で TLS を張り、暗号化と小さな書き込みで CPU を使う
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func envOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func envIntOrDefault(key string, defaultValue int) int {
	if value, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return value
	}
	return defaultValue
}

func envDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	if value, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return value
	}
	return defaultValue
}
