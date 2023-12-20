package handler

import (
	"net/http"

	"github.com/MicahParks/keyfunc"
	"github.com/hiendaovinh/toolkit/pkg/auth"
	"github.com/hiendaovinh/toolkit/pkg/httpx-echo"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/ory/ladon"
	ice_x "github.com/phinc275/ice-x/internal/ice-x"
)

func New(cfg *Config, manager ice_x.Manager) (http.Handler, error) {
	r := echo.New()
	cors := middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     cfg.Origins,
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization},
		AllowCredentials: true,
		MaxAge:           60 * 60,
	})
	r.Use(cors)
	r.Use(middleware.Recover())

	api := r.Group("/api/v1")
	group := XAccountGroup{manager: manager}

	adminRoutes := api.Group("/accounts")
	if cfg.Mode == "release" {
		authn, err := keyfunc.Get(cfg.Jwks, keyfunc.Options{})
		if err != nil {
			return nil, err
		}
		authz, err := auth.NewLadon([]ladon.Policy{
			&ladon.DefaultPolicy{
				ID:          "default policy",
				Description: `Default policy - no restriction here`,
				Subjects:    []string{"<.+>"},
				Resources:   []string{"<.+>"},
				Actions:     []string{"<.+>"},
				Effect:      ladon.AllowAccess,
			},
		})
		guard, err := auth.NewGuard(authn, authz)
		if err != nil {
			return nil, err
		}
		adminRoutes.Use(httpx.Authn(guard))
		adminRoutes.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := c.Request().Context()
				sub, err := auth.ResolveValidSubject(ctx)
				if err != nil {
					return httpx.RestAbort(c, nil, err)
				}
				for _, id := range cfg.AdminIDs {
					if id == sub {
						return next(c)
					}
				}
				return httpx.RestAbort(c, nil, auth.ErrInvalidSession)
			}
		})
	}

	adminRoutes.GET("/", group.List)
	adminRoutes.POST("/", group.Add)
	adminRoutes.DELETE("/:username", group.Delete)
	adminRoutes.PATCH("/:username", group.Update)
	adminRoutes.POST("/:username/flow", group.DoFlow)
	adminRoutes.POST("/:username/force-login", group.ForceLogin)
	adminRoutes.POST("/:username/refresh", group.Refresh)

	xGroup := api.Group("/x")
	xGroup.GET("/replies", group.Replies)
	xGroup.GET("/quotes", group.Quotes)
	xGroup.GET("/retweeters", group.Retweeters)
	xGroup.GET("/favoriters", group.Favoriters)
	xGroup.GET("/followings", group.Followings)
	xGroup.GET("/statuses", group.Statuses)

	return r, nil
}

type Config struct {
	Mode     string
	Jwks     string
	Origins  []string
	AdminIDs []string
}
