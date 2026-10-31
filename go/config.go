package isureal

import (
	"net"
	"net/url"
	"os"
	"strings"
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
	}
}

func (c *Config) DatabaseDSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Host:   net.JoinHostPort(c.DBHost, c.DBPort),
		Path:   "/" + c.DBName,
	}
	return u.String()
}

func envOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
