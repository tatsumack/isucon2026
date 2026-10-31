package isureal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type searchUsersRequest struct {
	Q              string `query:"q"`
	BeforeUsername string `query:"beforeUsername"`
	BeforeID       string `query:"beforeId"`
}

type searchUsersResponse struct {
	Items   []relationshipUserResponse `json:"items"`
	HasMore bool                       `json:"hasMore"`
}

func normalizeSearchText(value string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(value)))
}

// LIKEのエスケープ文字はデフォルトのバックスラッシュ。
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// refreshUserSearchColumns は、全ユーザーの search_username / search_display_name を
// normalizeSearchText で計算し直す。正規化はGoでしかできないため、/initialize で呼ぶ。
// reset.sql が表示名を初期値に戻したユーザーも、ここで正しい値になる。
func refreshUserSearchColumns(ctx context.Context, db *sqlx.DB) error {
	var users []struct {
		ID          string `db:"id"`
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
	}
	if err := db.SelectContext(ctx, &users, `SELECT id, username, display_name FROM users`); err != nil {
		return err
	}
	ids := make([]string, len(users))
	usernames := make([]string, len(users))
	displayNames := make([]string, len(users))
	for i, user := range users {
		ids[i] = user.ID
		usernames[i] = normalizeSearchText(user.Username)
		displayNames[i] = normalizeSearchText(user.DisplayName)
	}
	_, err := db.ExecContext(ctx, `
  UPDATE users u SET search_username = v.search_username, search_display_name = v.search_display_name
  FROM unnest($1::varchar[], $2::text[], $3::text[]) AS v(id, search_username, search_display_name)
  WHERE u.id = v.id
    AND (u.search_username, u.search_display_name) IS DISTINCT FROM (v.search_username, v.search_display_name)
 `, ids, usernames, displayNames)
	return err
}

func searchUsersHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input searchUsersRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if !validUserText(input.Q, 1, 100) {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		if (input.BeforeUsername == "") != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		pattern := "%" + likeEscaper.Replace(normalizeSearchText(input.Q)) + "%"
		var users []User
		if err := h.DB.SelectContext(ctx, &users, `SELECT `+strings.Join(userColumns, ", ")+` FROM users
  WHERE status='active' AND id<>$1 AND ($2='' OR (username,id)>($2,$3))
    AND (search_username LIKE $4 OR search_display_name LIKE $4)
    AND NOT EXISTS (
      SELECT 1 FROM block_relations b
      WHERE (b.blocker_id = $1 AND b.blocked_id = users.id) OR (b.blocked_id = $1 AND b.blocker_id = users.id)
    )
  ORDER BY username, id LIMIT 51`, viewer, input.BeforeUsername, input.BeforeID, pattern); err != nil {
			return err
		}
		items := make([]relationshipUserResponse, 0)
		for _, user := range users {
			if err := user.setImageKey(); err != nil {
				return err
			}
			if user.AccountType != "certified_public" {
				user.Bio = ""
			}
			items = append(items, newRelationshipUserResponse(user))
			if len(items) == 51 {
				break
			}
		}
		hasMore := len(items) > 50
		if hasMore {
			items = items[:50]
		}
		return c.JSON(http.StatusOK, searchUsersResponse{Items: items, HasMore: hasMore})
	}
}

type getUserRequest struct {
	UserID string `param:"userId"`
}

type getUserResponse relationshipUserResponse

func getUserHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getUserRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		user, err := getUser(ctx, h.DB, input.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		if err != nil {
			return err
		}
		blocked, err := isBlocked(ctx, h.DB, viewer, user.ID)
		if err != nil {
			return err
		}
		if blocked {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		friends, err := getFriendIDs(ctx, h.DB, viewer)
		if err != nil {
			return err
		}
		if user.AccountType != "certified_public" && !slices.Contains(friends, user.ID) {
			user.Bio = ""
		}
		return c.JSON(http.StatusOK, getUserResponse(newRelationshipUserResponse(user)))
	}
}

func newRelationshipUserResponse(user User) relationshipUserResponse {
	return relationshipUserResponse{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, ProfileImageURL: newGetMeResponse(user).ProfileImageURL, Bio: user.Bio, AccountType: user.AccountType}
}
