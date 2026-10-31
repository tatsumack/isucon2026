package isureal

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

type User struct {
	ID             string  `db:"id" json:"id"`
	Username       string  `db:"username" json:"username"`
	SecretKeyHash  string  `db:"secret_key_hash" json:"-"`
	DisplayName    string  `db:"display_name" json:"displayName"`
	Bio            string  `db:"bio" json:"bio"`
	ProfileImageID *string `db:"profile_image_id" json:"-"`
	Region         string  `db:"region" json:"region"`
	AccountType    string  `db:"account_type" json:"accountType"`
	Status         string  `db:"status" json:"status"`
	ImageKey       []byte  `db:"-" json:"-"`
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

func validUserText(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && length >= minimum && length <= maximum
}

func validRegion(region string) bool {
	switch region {
	case "america", "europe", "asia":
		return true
	default:
		return false
	}
}

var userColumns = []string{"id", "username", "secret_key_hash", "display_name", "bio", "profile_image_id", "region", "account_type", "status"}

func userColumnsAs(alias, prefix string) string {
	columns := make([]string, len(userColumns))
	for i, column := range userColumns {
		columns[i] = fmt.Sprintf(`%s.%s AS "%s.%s"`, alias, column, prefix, column)
	}
	return strings.Join(columns, ", ")
}

func (user *User) setImageKey() error {
	key, err := imageSigningKey(user.SecretKeyHash)
	user.ImageKey = key
	return err
}

// 利用者ID → 画像の署名鍵。投稿の処理で、DB から利用者を読む前に署名の依頼を出すために使う (post_api.go)。
// secret_key_hash は変更されず、ID は使い回されないので、値が古くなることはない。getUser で入れ、/initialize で空にする。
var imageKeyCache sync.Map

func cachedImageKey(userID string) ([]byte, bool) {
	key, ok := imageKeyCache.Load(userID)
	if !ok {
		return nil, false
	}
	return key.([]byte), true
}

// cacheImageKey は署名鍵を計算し、投稿の署名を先行依頼できるよう利用者IDで保持する。
// secret_key_hash は変更されず、利用者IDも使い回されないため、/initialize まで無効化は不要。
func cacheImageKey(user *User) error {
	if err := user.setImageKey(); err != nil {
		return err
	}
	imageKeyCache.Store(user.ID, user.ImageKey)
	return nil
}

func getUser(ctx context.Context, db sqlx.QueryerContext, userID string) (User, error) {
	var user User
	err := sqlx.GetContext(ctx, db, &user, `SELECT `+strings.Join(userColumns, ", ")+` FROM users WHERE id = $1 AND status = 'active'`, userID)
	if err != nil {
		return user, err
	}
	if err := cacheImageKey(&user); err != nil {
		return user, err
	}
	return user, nil
}
