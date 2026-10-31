package isureal

import (
	"context"
	"os"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type HandlerContext struct {
	Sessions        *SessionCache
	DB              *sqlx.DB
	Config          *Config
	IPBans          *IPBanList
	NotificationHub *NotificationHub
	Images          *ImageStore
	Signer          *ImageSigner
}

func NewServer(config *Config) *echo.Echo {
	if !validRegion(config.Region) {
		panic("invalid server region: " + config.Region)
	}
	e := echo.New()
	e.IPExtractor = extractClientIP

	e.Use(ActionLogMiddleware(config.LogLevel, os.Stdout))
	e.Use(middleware.Recover())
	e.HTTPErrorHandler = APIErrorHandler

	db, err := sqlx.Open("pgx", config.DatabaseDSN())
	if err != nil {
		panic("failed to open database: " + err.Error())
	}
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(30)
	err = db.Ping()
	if err != nil {
		panic("failed to ping database: " + err.Error())
	}

	handlerContext := &HandlerContext{
		DB:              db,
		Sessions:        newSessionCache(config),
		Config:          config,
		IPBans:          &IPBanList{},
		NotificationHub: NewNotificationHub(db),
		Images:          NewImageStore(config, db),
		Signer:          NewImageSigner(config),
	}
	go handlerContext.NotificationHub.listen(context.Background(), config.DatabaseDSN())
	e.Use(IPBanMiddleware(handlerContext))
	e.Use(PriorityMiddleware(handlerContext))
	// europe1 / america1 では、DB への往復が多い API を asia1 へ転送する (delegate.go)。
	// 転送しないもの: 画像の取得 (各サーバの画像キャッシュを使う)、通知 (SSE)、管理者 API
	// 登録・ログイン・ログアウト・自分の情報の更新・プロフィール画像も転送する (DB と約5往復するため)。
	// 認証状態の変更は asia1 に集約し、セッションキャッシュもそこで無効化する。
	// 退会・初期化も委譲する。海外のSSE認証はキャッシュせずDBの現在値を読む。
	// ログイン・登録の行動ログは、転送元では username が null になり、username つきの行は asia1 に出る
	// エクスポートは、画像本体が asia1 のディスクにあるため転送する (image_store.go)
	delegate := DelegateMiddleware(handlerContext)

	// asia1 の画像を europe1 / america1 から読む内部 API (image_store.go)。/api/ の外に置き、nginx からは届かない
	e.GET("/internal/images/:imageId", getInternalImageHandler(handlerContext), trustedPeerOnly(handlerContext))
	e.POST("/internal/images/reset", resetInternalImagesHandler(handlerContext), trustedPeerOnly(handlerContext))
	// asia1 から画像署名の計算を受ける内部 API (image_signature_remote.go)
	e.POST("/internal/image-signatures", postInternalImageSignaturesHandler(handlerContext), trustedPeerOnly(handlerContext))

	e.POST("/api/initialize", initializeHandler(handlerContext), delegate)
	e.POST("/api/auth/register", registerHandler(handlerContext), delegate)
	e.POST("/api/auth/login", loginHandler(handlerContext), delegate)
	e.POST("/api/auth/logout", logoutHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true))
	e.GET("/api/users", searchUsersHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/users/:userId", getUserHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/me/export", exportPostsHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/me", getMeHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.PATCH("/api/me", updateMeHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true))
	e.PUT("/api/me/profile-image", putProfileImageHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true))
	e.DELETE("/api/me/profile-image", deleteProfileImageHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true))
	e.POST("/api/me/account-deletion", deleteAccountHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.POST("/api/posts", createPostHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/posting-window/current", getCurrentPostingWindowHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/posts/me/current", getMyCurrentPostHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/posts/:postId", getPostHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.PATCH("/api/posts/:postId", updatePostHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.DELETE("/api/posts/:postId", deletePostHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/feeds/:feed", getFeedHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/memories", getMemoriesHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/memories/:date", getMemoriesByDateHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.POST("/api/posts/:postId/comments", createCommentHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/posts/:postId/comments", getCommentsHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.DELETE("/api/comments/:commentId", deleteCommentHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.POST("/api/posts/:postId/realmojis", replaceRealMojiHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/posts/:postId/realmojis", getRealMojisHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.DELETE("/api/realmojis/:realMojiId", deleteRealMojiHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/images/:imageId", getImageHandler(handlerContext))
	e.GET("/api/notifications", getNotificationsHandler(handlerContext), SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.POST("/api/friend-requests", createFriendRequestHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.POST("/api/friend-requests/:requestId/:action", transitionFriendRequestHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.GET("/api/relationships/:kind", getRelationshipsHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.DELETE("/api/friendships/:userId", deleteFriendshipHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.PUT("/api/follows/:userId", followHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.DELETE("/api/follows/:userId", unfollowHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.PUT("/api/blocks/:userId", blockHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.DELETE("/api/blocks/:userId", unblockHandler(handlerContext), delegate, SessionMiddleware(handlerContext, true), RegionMiddleware(handlerContext))
	e.POST("/api/admin/auth/login", adminLoginHandler(handlerContext))
	e.POST("/api/admin/auth/logout", adminLogoutHandler(handlerContext), AdminSessionMiddleware(handlerContext, true))
	e.PUT("/api/admin/users/:userId/certification", certifyUserHandler(handlerContext), AdminSessionMiddleware(handlerContext, true))
	e.DELETE("/api/admin/users/:userId/certification", deleteUserCertificationHandler(handlerContext), AdminSessionMiddleware(handlerContext, true))
	e.POST("/api/admin/posting-window", openPostingWindowHandler(handlerContext), AdminSessionMiddleware(handlerContext, true))

	return e
}
