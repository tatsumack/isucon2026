package isureal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type Session struct {
	Region    string    `db:"region" json:"-"`
	Token     string    `db:"-" json:"-"`
	UserID    string    `db:"user_id"`
	TokenHash string    `db:"token_hash"`
	CreatedAt time.Time `db:"created_at"`
}

func GetSession(c *echo.Context) *Session {
	sess, ok := c.Get("session").(*Session)
	if !ok {
		return nil
	}
	return sess
}

func getSessionFromHeader(c *echo.Context, db *sqlx.DB, cache *SessionCache) (*Session, error) {
	authHeader := c.Request().Header.Get(echo.HeaderAuthorization)
	if authHeader == "" {
		return nil, nil
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.Logger().Warn("Invalid authorization header format(not started with Bearer)")
		return nil, NewAPIError(http.StatusUnauthorized, "invalid_session")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")

	tokenHashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(tokenHashBytes[:])
	return cache.lookup(c.Request().Context(), tokenHash, func(ctx context.Context, hash string) (*Session, error) {
		return loadSession(ctx, db, hash)
	})
}

func loadSession(ctx context.Context, db *sqlx.DB, tokenHash string) (*Session, error) {
	var session Session
	err := db.GetContext(ctx, &session, `
		SELECT s.user_id, s.token_hash, s.created_at, u.region
		FROM login_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.active = true AND u.status = 'active'
	`, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	return &session, nil
}

func SessionMiddleware(handlerContext *HandlerContext, authRequired bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			session, err := getSessionFromHeader(c, handlerContext.DB, handlerContext.Sessions)
			if err != nil {
				return err
			}
			if session == nil && authRequired {
				return NewAPIError(http.StatusUnauthorized, "invalid_session")
			}
			// ベンチマーカーの行動分析用。nginxがログに出し、クライアントには返さない (proxy_hide_header)
			if session != nil {
				c.Response().Header().Set("X-User-Id", session.UserID)
			}
			c.Set("session", session)
			return next(c)
		}
	}
}

func InvalidateLoginSession(db *sqlx.DB, userID string, tokenHash string) error {
	tx, err := db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var lockedUserID string
	if err := tx.Get(&lockedUserID, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
		return fmt.Errorf("failed to lock user: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE login_sessions
		SET active = false, invalidated_at = clock_timestamp(), reason = 'logout'
		WHERE user_id = $1 AND active = true AND token_hash = $2	
	`, userID, tokenHash); err != nil {
		return fmt.Errorf("failed to invalidate sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func CreateLoginSession(db *sqlx.DB, userID string) (*Session, error) {
	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()
	session, err := createLoginSessionTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return session, nil
}

// ログインセッションの作成を、ユーザーごとに直列化する advisory lock の名前空間。
// 2引数版の advisory lock は1引数版 (posting_window_lock.go) とキー空間が分かれている。
const loginSessionLockClass = 1

// createLoginSessionTx は、ユーザーの有効なセッションを新しい1つに置き換える。
// ユーザーが有効でなければ sql.ErrNoRows を包んだエラーを返す。
//
// 「有効なセッションは1ユーザー1つ」を保つため、同じユーザーのログイン同士を advisory lock で直列化する。
// users の行ロックは取らない。投稿やコメントなど、本人の行をロックしたまま処理するトランザクションを待たないため。
// ロックを取った後の文は新しいスナップショットで実行されるので、先に終わったログインのセッションも無効化できる。
func createLoginSessionTx(ctx context.Context, tx *sqlx.Tx, userID string) (*Session, error) {
	randomToken := uuid.NewString()
	tokenHashBytes := sha256.Sum256([]byte(randomToken))
	tokenHash := hex.EncodeToString(tokenHashBytes[:])
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, loginSessionLockClass, userID); err != nil {
		return nil, fmt.Errorf("failed to lock login session: %w", err)
	}
	var session Session
	err := tx.GetContext(ctx, &session, `
  WITH invalidated AS (
    UPDATE login_sessions
    SET active = false, invalidated_at = clock_timestamp(), reason = 'replaced'
    WHERE user_id = $1 AND active = true
  )
  INSERT INTO login_sessions (id, user_id, token_hash)
  SELECT $2, $1, $3 WHERE EXISTS (SELECT 1 FROM users WHERE id = $1 AND status = 'active')
  RETURNING user_id, token_hash, created_at
 `, userID, uuid.NewString(), tokenHash)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	session.Token = randomToken
	return &session, nil
}
