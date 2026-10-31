package isureal

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestDatabaseDSNDisablesTLS(t *testing.T) {
	config := &Config{DBHost: "10.0.0.2", DBPort: "5432", DBUser: "isureal", DBPassword: "p@ss", DBName: "isureal"}
	parsed, err := pgx.ParseConfig(config.DatabaseDSN())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TLSConfig != nil {
		t.Fatalf("TLSConfig = %v, want nil", parsed.TLSConfig)
	}
	if len(parsed.Fallbacks) != 0 {
		t.Fatalf("Fallbacks = %d, want 0", len(parsed.Fallbacks))
	}
	if parsed.Host != "10.0.0.2" || parsed.User != "isureal" || parsed.Password != "p@ss" || parsed.Database != "isureal" {
		t.Fatalf("unexpected config: %+v", parsed)
	}
}
