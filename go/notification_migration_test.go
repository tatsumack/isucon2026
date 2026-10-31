package isureal

import (
	"os"
	"strings"
	"testing"
)

func TestNotificationMigrationMatchesSQL(t *testing.T) {
	data, err := os.ReadFile("../sql/005-event-notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(notificationHistoryMigrationSQL) {
		t.Fatal("起動migrationとSQL migrationが一致しない")
	}
}

func TestEventOnlyNotificationsOptIn(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "true"} {
		t.Setenv("ISUREAL_EVENT_ONLY_NOTIFICATIONS", value)
		if got := ConfigFromEnv().EventOnlyNotifications; got != (value == "true") {
			t.Fatalf("value=%q enabled=%v", value, got)
		}
	}
}
