package isureal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type registerRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Region      string `json:"region"`
}

type registerResponse struct {
	SecretKey string              `json:"secretKey"`
	User      sessionUserResponse `json:"user"`
}

type loginRequest struct {
	Username  string `json:"username"`
	SecretKey string `json:"secretKey"`
}

type loginResponse struct {
	Token string              `json:"token"`
	User  sessionUserResponse `json:"user"`
}

type sessionUserResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Bio         string `json:"bio"`
	Region      string `json:"region"`
	AccountType string `json:"accountType"`
	Status      string `json:"status"`
}

type getMeResponse struct {
	sessionUserResponse
	ProfileImageURL *string `json:"profileImageUrl"`
}

type updateMeRequest struct {
	DisplayName *string `json:"displayName"`
	Bio         *string `json:"bio"`
	Region      *string `json:"region"`
}

type updateMeResponse getMeResponse

func newSessionUserResponse(user User) sessionUserResponse {
	return sessionUserResponse{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
		Bio: user.Bio, Region: user.Region, AccountType: user.AccountType, Status: user.Status,
	}
}

func newGetMeResponse(user User) getMeResponse {
	response := getMeResponse{sessionUserResponse: newSessionUserResponse(user)}
	if user.ProfileImageID != nil {
		url := signedImageURL(user, *user.ProfileImageID)
		response.ProfileImageURL = &url
	}
	return response
}

func registerHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input registerRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		c.Set("username", input.Username)
		if !usernamePattern.MatchString(input.Username) || !validUserText(input.DisplayName, 1, 100) || !validRegion(input.Region) {
			return NewAPIError(http.StatusBadRequest, "invalid_registration")
		}
		secretKey := uuid.NewString()
		hash := sha256.Sum256([]byte(secretKey))
		ctx := c.Request().Context()
		tx, err := h.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// 投稿期間開始時の通知対象と、登録の前後関係をリージョン単位で確定する。
		if err := lockPostingWindowRegionsShared(ctx, tx, input.Region); err != nil {
			return err
		}
		userID := uuid.NewString()
		var user User
		err = tx.GetContext(ctx, &user, `
			INSERT INTO users (id, username, secret_key_hash, display_name, region, search_username, search_display_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (username) DO NOTHING
			RETURNING `+strings.Join(userColumns, ", ")+`
		`, userID, input.Username, hex.EncodeToString(hash[:]), input.DisplayName, input.Region,
			normalizeSearchText(input.Username), normalizeSearchText(input.DisplayName))
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusConflict, "username_taken")
		}
		if err != nil {
			return err
		}
		// 通知方式を旧方式へ戻しても、将来の cut に必要な履歴の追跡は止めない。
		if err := recordMembershipState(ctx, tx, user.ID, user.Region, true); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, registerResponse{SecretKey: secretKey, User: newSessionUserResponse(user)})
	}
}

func loginHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input loginRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		c.Set("username", input.Username)
		if !usernamePattern.MatchString(input.Username) {
			return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
		}
		ctx := c.Request().Context()
		// 認証はロックもトランザクションもなしで先に行う。失敗するログインが多く、それらでDBを待たせないため。
		// secret_key_hash は変更されない。status は createLoginSessionTx の中でもう一度確認する。
		var user User
		err := h.DB.GetContext(ctx, &user, `SELECT id, username, secret_key_hash, display_name, bio, profile_image_id, region, account_type, status FROM users WHERE username = $1`, input.Username)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
		}
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(input.SecretKey))
		if user.Status != "active" || user.SecretKeyHash != hex.EncodeToString(hash[:]) {
			return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
		}
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		session, err := createLoginSessionTx(ctx, tx, user.ID)
		if errors.Is(err, sql.ErrNoRows) {
			// 認証の後に退会した
			return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
		}
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, loginResponse{Token: session.Token, User: newSessionUserResponse(user)})
	}
}

func logoutHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		session := GetSession(c)
		if session == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_session")
		}
		if err := InvalidateLoginSession(h.DB, session.UserID, session.TokenHash); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func getMeHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		session := GetSession(c)
		if session == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_session")
		}
		user, err := getUser(c.Request().Context(), h.DB, session.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, newGetMeResponse(user))
	}
}

func updateMeHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		session := GetSession(c)
		if session == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_session")
		}
		var input updateMeRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.DisplayName == nil && input.Bio == nil && input.Region == nil) ||
			(input.DisplayName != nil && !validUserText(*input.DisplayName, 1, 100)) ||
			(input.Bio != nil && !validUserText(*input.Bio, 0, 500)) ||
			(input.Region != nil && !validRegion(*input.Region)) {
			return NewAPIError(http.StatusBadRequest, "invalid_profile")
		}
		ctx := c.Request().Context()
		tx, err := h.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var currentRegion string
		if err := tx.GetContext(ctx, &currentRegion, `SELECT region FROM users WHERE id = $1 AND status = 'active' FOR NO KEY UPDATE`, session.UserID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NewAPIError(http.StatusNotFound, "account_not_found")
			}
			return err
		}
		if input.Region != nil && *input.Region != currentRegion {
			if err := lockPostingWindowRegionsShared(ctx, tx, currentRegion, *input.Region); err != nil {
				return err
			}
		}
		var normalizedDisplayName *string
		if input.DisplayName != nil {
			normalized := normalizeSearchText(*input.DisplayName)
			normalizedDisplayName = &normalized
		}
		var user User
		err = tx.GetContext(ctx, &user, `
			UPDATE users SET
				display_name = COALESCE($2::text, display_name),
				search_display_name = COALESCE($3::text, search_display_name),
				bio = COALESCE($4::text, bio),
				region = COALESCE($5::text, region),
				updated_at = clock_timestamp()
			WHERE id = $1 AND status = 'active'
			RETURNING `+strings.Join(userColumns, ", ")+`
		`, session.UserID, input.DisplayName, normalizedDisplayName, input.Bio, input.Region)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		if err != nil {
			return err
		}
		if input.Region != nil && *input.Region != currentRegion {
			if err := recordMembershipState(ctx, tx, user.ID, currentRegion, false); err != nil {
				return err
			}
			if err := recordMembershipState(ctx, tx, user.ID, user.Region, true); err != nil {
				return err
			}
		}
		if err := user.setImageKey(); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, updateMeResponse(newGetMeResponse(user)))
	}
}
