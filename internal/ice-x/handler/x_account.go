package handler

import (
	"github.com/hiendaovinh/toolkit/pkg/errorx"
	"github.com/hiendaovinh/toolkit/pkg/httpx-echo"
	"github.com/labstack/echo/v4"
	ice_x "github.com/phinc275/ice-x/internal/ice-x"
)

type XAccountGroup struct {
	manager ice_x.Manager
}

func (group *XAccountGroup) List(c echo.Context) error {
	ctx := c.Request().Context()

	accounts, err := group.manager.List(ctx)
	return httpx.RestAbort(c, accounts, err)
}

func (group *XAccountGroup) Add(c echo.Context) error {
	ctx := c.Request().Context()
	var payload ice_x.CreateAccountPayload
	err := c.Bind(&payload)
	if err != nil {
		return httpx.RestAbort(c, nil, errorx.Wrap(err, errorx.Invalid))
	}

	err = group.manager.Add(ctx, payload)
	return httpx.RestAbort(c, nil, err)
}

func (group *XAccountGroup) Update(c echo.Context) error {
	ctx := c.Request().Context()
	var payload ice_x.UpdateAccountPayload
	err := c.Bind(&payload)
	if err != nil {
		return httpx.RestAbort(c, nil, errorx.Wrap(err, errorx.Invalid))
	}

	payload.Username = c.Param("username")
	err = group.manager.Update(ctx, payload)
	return httpx.RestAbort(c, nil, err)
}

func (group *XAccountGroup) Delete(c echo.Context) error {
	ctx := c.Request().Context()
	err := group.manager.Delete(ctx, c.Param("username"))
	return httpx.RestAbort(c, nil, err)
}

func (group *XAccountGroup) DoFlow(c echo.Context) error {
	ctx := c.Request().Context()
	var payload ice_x.DoFlowPayload
	err := c.Bind(&payload)
	if err != nil {
		return httpx.RestAbort(c, nil, errorx.Wrap(err, errorx.Invalid))
	}
	payload.Username = c.Param("username")
	err = group.manager.DoFlow(ctx, payload)
	return httpx.RestAbort(c, nil, err)
}

func (group *XAccountGroup) ForceLogin(c echo.Context) error {
	ctx := c.Request().Context()
	err := group.manager.ForceLogin(ctx, c.Param("username"))
	return httpx.RestAbort(c, nil, err)
}

func (group *XAccountGroup) Refresh(c echo.Context) error {
	ctx := c.Request().Context()
	err := group.manager.Refresh(ctx, c.Param("username"))
	return httpx.RestAbort(c, nil, err)
}

func (group *XAccountGroup) Replies(c echo.Context) error {
	q := c.QueryParams()
	tweetID := q.Get("tweetID")
	cursor := q.Get("cursor")

	replies, nextCursor, err := group.manager.Replies(c.Request().Context(), tweetID, cursor)
	return httpx.RestAbort(c, struct {
		Items      []ice_x.Reply `json:"items"`
		NextCursor string        `json:"next_cursor,omitempty"`
	}{
		Items:      replies,
		NextCursor: nextCursor,
	}, err)
}

func (group *XAccountGroup) Quotes(c echo.Context) error {
	q := c.QueryParams()
	tweetID := q.Get("tweetID")
	cursor := q.Get("cursor")

	quotes, nextCursor, err := group.manager.Quotes(c.Request().Context(), tweetID, cursor)
	return httpx.RestAbort(c, struct {
		Items      []ice_x.Quote `json:"items"`
		NextCursor string        `json:"next_cursor,omitempty"`
	}{
		Items:      quotes,
		NextCursor: nextCursor,
	}, err)
}

func (group *XAccountGroup) Retweeters(c echo.Context) error {
	q := c.QueryParams()
	tweetID := q.Get("tweetID")
	cursor := q.Get("cursor")

	retweeters, nextCursor, err := group.manager.Retweets(c.Request().Context(), tweetID, cursor)
	return httpx.RestAbort(c, struct {
		Items      []ice_x.Retweet `json:"items"`
		NextCursor string          `json:"next_cursor,omitempty"`
	}{
		Items:      retweeters,
		NextCursor: nextCursor,
	}, err)
}

func (group *XAccountGroup) Favoriters(c echo.Context) error {
	q := c.QueryParams()
	tweetID := q.Get("tweetID")
	cursor := q.Get("cursor")

	favoriters, nextCursor, err := group.manager.Likes(c.Request().Context(), tweetID, cursor)
	return httpx.RestAbort(c, struct {
		Items      []ice_x.Like `json:"items"`
		NextCursor string       `json:"next_cursor,omitempty"`
	}{
		Items:      favoriters,
		NextCursor: nextCursor,
	}, err)
}

func (group *XAccountGroup) Followings(c echo.Context) error {
	q := c.QueryParams()
	userID := q.Get("userID")
	cursor := q.Get("cursor")

	followings, nextCursor, err := group.manager.Following(c.Request().Context(), userID, cursor)
	return httpx.RestAbort(c, struct {
		Items      []ice_x.Following `json:"items"`
		NextCursor string            `json:"next_cursor,omitempty"`
	}{
		Items:      followings,
		NextCursor: nextCursor,
	}, err)
}

func (group *XAccountGroup) Statuses(c echo.Context) error {
	q := c.QueryParams()
	screenName := q.Get("screenName")
	cursor := q.Get("cursor")

	statuses, nextCursor, err := group.manager.StatusesByScreenName(c.Request().Context(), screenName, cursor)
	return httpx.RestAbort(c, struct {
		Items      []ice_x.StatusStat `json:"items"`
		NextCursor string             `json:"next_cursor,omitempty"`
	}{
		Items:      statuses,
		NextCursor: nextCursor,
	}, err)
}
