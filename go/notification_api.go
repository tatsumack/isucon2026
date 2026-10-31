package isureal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

type getNotificationsResponse struct {
	ID              string    `json:"id" db:"id"`
	PostingWindowID string    `json:"postingWindowId" db:"posting_window_id"`
	Region          string    `json:"region" db:"region"`
	CreatedAt       time.Time `json:"createdAt" db:"created_at"`
}

func getNotificationsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		session := GetSession(c)
		userID := session.UserID
		var connection struct {
			ConnectedAt time.Time `db:"connected_at"`
			Generation  int64     `db:"generation"`
		}
		if err := h.DB.GetContext(ctx, &connection, `SELECT clock_timestamp() AS connected_at, generation FROM notification_history_state WHERE id = true`); err != nil {
			return err
		}
		response := c.Response()
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(response)
		if err := controller.Flush(); err != nil {
			return nil
		}
		events, unsubscribe := h.NotificationHub.Subscribe(userID, session.Region, connection.ConnectedAt, connection.Generation)
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return nil
			case event := <-events:
				if event.Err != nil {
					c.Logger().Error("failed to poll notifications", "error", event.Err)
					return nil
				}
				if event.Disconnect {
					return nil
				}
				data, err := json.Marshal(event.Notification)
				if err != nil {
					c.Logger().Error("failed to encode notification", "error", err)
					return nil
				}
				if _, err := fmt.Fprintf(response, "data: %s\n\n", data); err != nil {
					return nil
				}
				_ = controller.Flush()
				return nil
			}
		}
	}
}
