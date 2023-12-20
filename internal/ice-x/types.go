package ice_x

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aarondl/opt/omit"
	"github.com/aarondl/opt/omitnull"
	"github.com/hiendaovinh/toolkit/pkg/errorx"
	"github.com/phinc275/ice-x/internal/ice-x/bob"
)

type Manager interface {
	List(ctx context.Context) ([]XAccountDTO, error)
	LoadFromCookies(ctx context.Context) error
	Add(ctx context.Context, payload CreateAccountPayload) error
	Update(ctx context.Context, payload UpdateAccountPayload) error
	Delete(ctx context.Context, username string) error
	DoFlow(ctx context.Context, payload DoFlowPayload) error
	ForceLogin(ctx context.Context, username string) error
	Refresh(ctx context.Context, username string) error

	Replies(ctx context.Context, tweetID string, cursor string) ([]Reply, string, error)
	Quotes(ctx context.Context, tweetID string, cursor string) ([]Quote, string, error)
	Retweets(ctx context.Context, tweetID string, cursor string) ([]Retweet, string, error)
	Likes(ctx context.Context, tweetID string, cursor string) ([]Like, string, error)
	Following(ctx context.Context, targetID string, cursor string) ([]Following, string, error)
	StatusesByScreenName(ctx context.Context, userID string, cursor string) ([]StatusStat, string, error)
}

func NewManager(xAccountDs XAccountDatastore) Manager {
	return &XManager{ds: xAccountDs, clients: make(map[string]*XClient), rwMtx: &sync.RWMutex{}}
}

type XAccount struct {
	Username                  string    `json:"username"`
	Password                  string    `json:"password"`
	Email                     string    `json:"email"`
	EmailPassword             string    `json:"email_password"`
	AutoEmailConfirmationCode bool      `json:"auto_email_confirmation_code"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`

	Status  string `json:"status"`
	Cookies string `json:"cookies"`
}

type XAccountSetter bob.XAccountSetter

type XAccountDatastore interface {
	FindAll(ctx context.Context) ([]*XAccount, error)
	FindOneByUsername(ctx context.Context, username string) (*XAccount, error)
	Create(ctx context.Context, account *XAccountSetter) (*XAccount, error)
	Update(ctx context.Context, account *XAccountSetter) (*XAccount, error)
	Delete(ctx context.Context, username string) error
}

type XManager struct {
	ds      XAccountDatastore
	clients map[string]*XClient
	rwMtx   *sync.RWMutex
}

var (
	twitterURL, _      = url.Parse("https://twitter.com/")
	defaultBearerToken = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
	cursorTypes        = map[string]bool{"Bottom": true, "ShowMoreThreads": true, "ShowMoreThreadsPrompt": true}
	regexUserEntryID   = regexp.MustCompile(`user-(\d+)`)
)

func (manager *XManager) LoadFromCookies(ctx context.Context) error {
	accounts, err := manager.ds.FindAll(ctx)
	if err != nil {
		return err
	}

	for _, account := range accounts {
		if account.Cookies == "" {
			manager.clients[account.Username] = &XClient{
				Account:     account,
				HttpClient:  nil,
				Status:      XClientStatusLoaded,
				Endpoints:   nil,
				ErrorReason: "",
			}
			continue
		}

		header := http.Header{}
		header.Add("Cookie", account.Cookies)
		req := http.Request{Header: header}
		cookies := req.Cookies()
		for _, c := range cookies {
			c.Path = "/"
			c.Domain = ".twitter.com"
		}

		jar, _ := cookiejar.New(nil)
		jar.SetCookies(twitterURL, cookies)
		httpClient := &http.Client{Jar: jar}
		client := &XClient{
			Account:    account,
			HttpClient: httpClient,
			Status:     XClientStatusLoggedIn,
			Endpoints:  DefaultEndpoints(),
		}
		client.FetchLimits(ctx)

		manager.clients[account.Username] = client
	}

	return nil
}

func (manager *XManager) List(ctx context.Context) ([]XAccountDTO, error) {
	manager.rwMtx.RLock()
	defer manager.rwMtx.RUnlock()

	ch := make(chan []XAccountDTO)
	go func() {
		accounts := make([]XAccountDTO, 0, len(manager.clients))
		for _, client := range manager.clients {
			endpoints := make([]XEndpointDTO, 0, len(client.Endpoints))
			for ek, ev := range client.Endpoints {
				endpoints = append(endpoints, XEndpointDTO{
					ID:          string(ek),
					CallLimit:   ev.CallLimit,
					Status:      ev.Status,
					Pending:     ev.Pending,
					Remaining:   ev.Remaining,
					Reset:       ev.Reset,
					ErrorReason: ev.ErrorReason,
				})
			}
			acc := XAccountDTO{
				Username:                  client.Account.Username,
				Email:                     client.Account.Email,
				HasPassword:               client.Account.Password != "",
				HasEmailPassword:          client.Account.EmailPassword != "",
				HasCookies:                client.Account.Cookies != "",
				Status:                    client.Status,
				ErrorReason:               client.ErrorReason,
				AutoEmailConfirmationCode: client.Account.AutoEmailConfirmationCode,
				Endpoints:                 endpoints,
			}
			accounts = append(accounts, acc)
		}
		select {
		case ch <- accounts:
		case <-time.After(time.Second):
			// assume that:
			// - 1 second is enough for parent routine to reach the select statement below
			// - if the timer passed, consumer no longer interest in result anymore
		}

		close(ch)
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case accounts := <-ch:
		return accounts, nil
	}
}

func (manager *XManager) Add(ctx context.Context, payload CreateAccountPayload) error {
	if !payload.Password.IsSet() && !payload.Cookies.IsSet() {
		return errorx.Wrap(fmt.Errorf("either password or cookies must be specified"), errorx.Invalid)
	}

	if payload.AutoEmailConfirmationCode {
		if !payload.Email.IsSet() || !strings.HasSuffix(payload.Email.MustGet(), "@hotmail.com") || !payload.EmailPassword.IsSet() {
			return errorx.Wrap(fmt.Errorf("auto email confirmation code is enabled but email data is missing or invalid"), errorx.Invalid)
		}
	}

	_, err := manager.ds.FindOneByUsername(ctx, payload.Username)
	if err == nil {
		return errorx.Wrap(fmt.Errorf("username %s already exists", payload.Username), errorx.Invalid)
	}
	if !errorx.IsNoRows(err) {
		return err
	}

	now := time.Now()
	account, err := manager.ds.Create(ctx, &XAccountSetter{
		Username:                  omit.From(payload.Username),
		Password:                  payload.Password,
		Email:                     payload.Email,
		EmailPassword:             payload.EmailPassword,
		CreatedAt:                 omit.From(now),
		UpdatedAt:                 omit.From(now),
		Cookies:                   payload.Cookies,
		AutoEmailConfirmationCode: omit.From(payload.AutoEmailConfirmationCode),
	})
	if err != nil {
		return err
	}

	var client *XClient
	if payload.TryLogin {
		client = tryLogin(ctx, account)
	} else {
		client = &XClient{
			Account:     account,
			HttpClient:  nil,
			Status:      XClientStatusLoaded,
			Endpoints:   nil,
			ErrorReason: "",
		}
	}

	if client.Status == XClientStatusLoggedIn && client.HttpClient != nil && client.HttpClient.Jar != nil {
		cookies := client.HttpClient.Jar.Cookies(twitterURL)
		cookieArr := make([]string, 0, len(cookies))
		for _, c := range cookies {
			cookieArr = append(cookieArr, c.String())
		}
		_, err = manager.ds.Update(ctx, &XAccountSetter{
			Username:  omit.From(account.Username),
			UpdatedAt: omit.From(time.Now()),
			Cookies:   omitnull.From(strings.Join(cookieArr, ";")),
		})
		if err != nil {
			return err
		}

		client.FetchLimits(ctx)
	}

	manager.rwMtx.Lock()
	manager.clients[payload.Username] = client
	manager.rwMtx.Unlock()

	return nil
}

func (manager *XManager) Update(ctx context.Context, payload UpdateAccountPayload) error {
	account, err := manager.ds.FindOneByUsername(ctx, payload.Username)
	if err != nil {
		return err
	}

	if !payload.Password.IsUnset() {
		account.Password = payload.Password.GetOrZero()
	}

	if !payload.Email.IsUnset() {
		account.Email = payload.Email.GetOrZero()
	}

	if !payload.EmailPassword.IsUnset() {
		account.EmailPassword = payload.EmailPassword.GetOrZero()
	}

	if !payload.Cookies.IsUnset() {
		account.Cookies = payload.Cookies.GetOrZero()
	}

	if !payload.AutoEmailConfirmationCode.IsUnset() {
		account.AutoEmailConfirmationCode = payload.AutoEmailConfirmationCode.GetOrZero()
	}

	if account.Password == "" && account.Cookies == "" {
		return errorx.Wrap(fmt.Errorf("either password or cookies must be specified"), errorx.Invalid)
	}

	if account.AutoEmailConfirmationCode {
		if account.Email == "" || !strings.HasSuffix(account.Email, "@hotmail.com") || account.EmailPassword == "" {
			return errorx.Wrap(fmt.Errorf("auto email confirmation code is enabled but email data is missing or invalid"), errorx.Invalid)
		}
	}

	manager.rwMtx.Lock()
	delete(manager.clients, payload.Username)
	manager.rwMtx.Unlock()

	updatedAccount, err := manager.ds.Update(ctx, &XAccountSetter{})
	if err != nil {
		return err
	}

	var client *XClient
	if payload.TryLogin {
		client = tryLogin(ctx, updatedAccount)
	} else {
		client = &XClient{
			Account:     updatedAccount,
			HttpClient:  nil,
			Status:      XClientStatusLoaded,
			Endpoints:   nil,
			ErrorReason: "",
		}
	}

	if client.Status == XClientStatusLoggedIn && client.HttpClient != nil && client.HttpClient.Jar != nil {
		cookies := client.HttpClient.Jar.Cookies(twitterURL)
		cookieArr := make([]string, 0, len(cookies))
		for _, c := range cookies {
			cookieArr = append(cookieArr, c.String())
		}
		_, err = manager.ds.Update(ctx, &XAccountSetter{
			Username:  omit.From(account.Username),
			UpdatedAt: omit.From(time.Now()),
			Cookies:   omitnull.From(strings.Join(cookieArr, ";")),
		})
		if err != nil {
			return err
		}

		client.FetchLimits(ctx)
	}

	manager.rwMtx.Lock()
	manager.clients[payload.Username] = client
	manager.rwMtx.Unlock()

	return nil
}

func (manager *XManager) Delete(ctx context.Context, username string) error {
	err := manager.ds.Delete(ctx, username)
	if err != nil {
		return err
	}

	manager.rwMtx.Lock()
	delete(manager.clients, username)
	manager.rwMtx.Unlock()

	return nil
}

func (manager *XManager) DoFlow(ctx context.Context, payload DoFlowPayload) error {
	manager.rwMtx.Lock()
	defer manager.rwMtx.Unlock()

	client := manager.clients[payload.Username]
	if client == nil || client.HttpClient == nil || client.HttpClient.Jar == nil || (client.Status != XClientStatusWaitingForEmail && client.Status != XClientStatusWaitingForConfirmationCode) {
		return errorx.Wrap(fmt.Errorf("account not found, in an invalid state, or not in waiting status"), errorx.Invalid)
	}

	var err error
	switch client.Status {
	case XClientStatusWaitingForEmail:
		_, err = confirmEmail(ctx, client.HttpClient, client.FlowToken, payload.Input)
	case XClientStatusWaitingForConfirmationCode:
		_, err = confirmationCode(ctx, client.HttpClient, client.FlowToken, payload.Input)
	}

	if err != nil {
		client.Status = XClientStatusErrored
		client.ErrorReason = err.Error()
		client.FlowToken = ""
	}

	client.Endpoints = DefaultEndpoints()
	client.FetchLimits(ctx)

	return nil
}

func (manager *XManager) ForceLogin(ctx context.Context, username string) error {
	manager.rwMtx.Lock()
	defer manager.rwMtx.Unlock()

	client := manager.clients[username]
	if client == nil || client.Account == nil {
		return errorx.Wrap(fmt.Errorf("account not found"), errorx.Invalid)
	}

	if client.Account.Password == "" {
		return errorx.Wrap(fmt.Errorf("no password"), errorx.Invalid)
	}

	cloneAccount := *client.Account
	cloneAccount.Cookies = ""

	client = tryLogin(ctx, &cloneAccount)
	if client.Status == XClientStatusLoggedIn && client.HttpClient != nil && client.HttpClient.Jar != nil {
		cookies := client.HttpClient.Jar.Cookies(twitterURL)
		cookieArr := make([]string, 0, len(cookies))
		for _, c := range cookies {
			cookieArr = append(cookieArr, c.String())
		}
		_, err := manager.ds.Update(ctx, &XAccountSetter{
			Username:  omit.From(cloneAccount.Username),
			UpdatedAt: omit.From(time.Now()),
			Cookies:   omitnull.From(strings.Join(cookieArr, ";")),
		})
		if err != nil {
			return err
		}

		client.FetchLimits(ctx)
	}

	manager.clients[username] = client
	return nil
}

func (manager *XManager) Refresh(ctx context.Context, username string) error {
	manager.rwMtx.Lock()
	defer manager.rwMtx.Unlock()

	client := manager.clients[username]
	if client == nil || client.Account == nil {
		return errorx.Wrap(fmt.Errorf("account not found"), errorx.Invalid)
	}

	if client.Status != XClientStatusLoggedIn {
		return errorx.Wrap(fmt.Errorf("account is not logged in properly"), errorx.Invalid)
	}

	client.FetchLimits(ctx)
	return nil
}

type (
	CreateAccountPayload struct {
		AccountPayload
		AutoEmailConfirmationCode bool `json:"auto_email_confirmation_code"`
		TryLogin                  bool `json:"try_login"`
	}
	UpdateAccountPayload struct {
		AccountPayload
		TryLogin bool `json:"try_login"`
	}
	DoFlowPayload struct {
		Username string `json:"-"`
		Input    string `json:"input"`
	}
)

type AccountPayload struct {
	Username                  string               `json:"username"`
	Password                  omitnull.Val[string] `json:"password"`
	Email                     omitnull.Val[string] `json:"email"`
	EmailPassword             omitnull.Val[string] `json:"email_password"`
	Cookies                   omitnull.Val[string] `json:"cookies"`
	AutoEmailConfirmationCode omit.Val[bool]       `json:"auto_email_confirmation_code"`
}

type FixLoginPayload struct{}

func (manager *XManager) FixLogin(ctx context.Context, payload FixLoginPayload) error {
	return nil
}

type XEndpointDTO struct {
	ID          string `json:"id"`
	CallLimit   int64  `json:"call_limit"`
	Status      string `json:"status"`
	Pending     int64  `json:"pending"`
	Remaining   int64  `json:"remaining"`
	Reset       int64  `json:"reset"`
	ErrorReason string `json:"error_reason"`
}

type XAccountDTO struct {
	Username                  string         `json:"username"`
	Email                     string         `json:"email"`
	HasPassword               bool           `json:"has_password"`
	HasEmailPassword          bool           `json:"has_email_password"`
	HasCookies                bool           `json:"has_cookies"`
	Status                    string         `json:"status"`
	ErrorReason               string         `json:"error_reason"`
	AutoEmailConfirmationCode bool           `json:"auto_email_confirmation_code"`
	Endpoints                 []XEndpointDTO `json:"endpoints"`
}

var (
	XClientStatusLoaded                     = "loaded"
	XClientStatusLoggedIn                   = "logged-in"
	XClientStatusErrored                    = "errored"
	XClientStatusWaitingForEmail            = "waiting-for-email"
	XClientStatusWaitingForConfirmationCode = "waiting-for-confirmation-code"

	XEndpointStatusOk        = "ok"
	XEndpointStatusErrored   = "errored"
	XEndpointStatusForbidden = "forbidden"
)

var (
	XBaseEndpointSearchTimeline = &XBaseEndpoint{
		BaseURL:   "https://twitter.com/i/api/graphql/tOUz374Df84NaVVr3M1p6g/SearchTimeline?variables=%7B%22rawQuery%22%3A%22quoted_tweet_id%3A1701892872574996627%22%2C%22count%22%3A20%2C%22querySource%22%3A%22tdqt%22%2C%22product%22%3A%22Top%22%7D&features=%7B%22responsive_web_graphql_exclude_directive_enabled%22%3Atrue%2C%22verified_phone_label_enabled%22%3Afalse%2C%22responsive_web_home_pinned_timelines_enabled%22%3Atrue%2C%22creator_subscriptions_tweet_preview_api_enabled%22%3Atrue%2C%22responsive_web_graphql_timeline_navigation_enabled%22%3Atrue%2C%22responsive_web_graphql_skip_user_profile_image_extensions_enabled%22%3Afalse%2C%22c9s_tweet_anatomy_moderator_badge_enabled%22%3Atrue%2C%22tweetypie_unmention_optimization_enabled%22%3Atrue%2C%22responsive_web_edit_tweet_api_enabled%22%3Atrue%2C%22graphql_is_translatable_rweb_tweet_is_translatable_enabled%22%3Atrue%2C%22view_counts_everywhere_api_enabled%22%3Atrue%2C%22longform_notetweets_consumption_enabled%22%3Atrue%2C%22responsive_web_twitter_article_tweet_consumption_enabled%22%3Afalse%2C%22tweet_awards_web_tipping_enabled%22%3Afalse%2C%22freedom_of_speech_not_reach_fetch_enabled%22%3Atrue%2C%22standardized_nudges_misinfo%22%3Atrue%2C%22tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled%22%3Atrue%2C%22longform_notetweets_rich_text_read_enabled%22%3Atrue%2C%22longform_notetweets_inline_media_enabled%22%3Atrue%2C%22responsive_web_media_download_video_enabled%22%3Afalse%2C%22responsive_web_enhance_cards_enabled%22%3Afalse%7D",
		CallLimit: 50,
		DefaultVariables: map[string]interface{}{
			"rawQuery":    "quoted_tweet_id:1701892872574996627",
			"count":       20,
			"querySource": "tdqt",
			"product":     "Top",
		},
		DefaultFeatures: map[string]interface{}{
			"responsive_web_graphql_exclude_directive_enabled":                        true,
			"verified_phone_label_enabled":                                            false,
			"creator_subscriptions_tweet_preview_api_enabled":                         true,
			"responsive_web_graphql_timeline_navigation_enabled":                      true,
			"responsive_web_graphql_skip_user_profile_image_extensions_enabled":       false,
			"tweetypie_unmention_optimization_enabled":                                true,
			"responsive_web_edit_tweet_api_enabled":                                   true,
			"graphql_is_translatable_rweb_tweet_is_translatable_enabled":              true,
			"view_counts_everywhere_api_enabled":                                      true,
			"longform_notetweets_consumption_enabled":                                 true,
			"responsive_web_twitter_article_tweet_consumption_enabled":                false,
			"tweet_awards_web_tipping_enabled":                                        false,
			"freedom_of_speech_not_reach_fetch_enabled":                               true,
			"standardized_nudges_misinfo":                                             true,
			"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
			"longform_notetweets_rich_text_read_enabled":                              true,
			"longform_notetweets_inline_media_enabled":                                true,
			"responsive_web_media_download_video_enabled":                             false,
			"responsive_web_enhance_cards_enabled":                                    false,
		},
		AdditionalParams: nil,
	}
	XBaseEndpointRetweeters = &XBaseEndpoint{
		BaseURL:   "https://twitter.com/i/api/graphql/sOBhVzDeJl4XGepvi5pHlg/Retweeters?variables=%7B%22tweetId%22%3A%221701892872574996627%22%2C%22count%22%3A20%2C%22includePromotedContent%22%3Atrue%7D&features=%7B%22responsive_web_graphql_exclude_directive_enabled%22%3Atrue%2C%22verified_phone_label_enabled%22%3Afalse%2C%22creator_subscriptions_tweet_preview_api_enabled%22%3Atrue%2C%22responsive_web_graphql_timeline_navigation_enabled%22%3Atrue%2C%22responsive_web_graphql_skip_user_profile_image_extensions_enabled%22%3Afalse%2C%22c9s_tweet_anatomy_moderator_badge_enabled%22%3Atrue%2C%22tweetypie_unmention_optimization_enabled%22%3Atrue%2C%22responsive_web_edit_tweet_api_enabled%22%3Atrue%2C%22graphql_is_translatable_rweb_tweet_is_translatable_enabled%22%3Atrue%2C%22view_counts_everywhere_api_enabled%22%3Atrue%2C%22longform_notetweets_consumption_enabled%22%3Atrue%2C%22responsive_web_twitter_article_tweet_consumption_enabled%22%3Afalse%2C%22tweet_awards_web_tipping_enabled%22%3Afalse%2C%22freedom_of_speech_not_reach_fetch_enabled%22%3Atrue%2C%22standardized_nudges_misinfo%22%3Atrue%2C%22tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled%22%3Atrue%2C%22rweb_video_timestamps_enabled%22%3Atrue%2C%22longform_notetweets_rich_text_read_enabled%22%3Atrue%2C%22longform_notetweets_inline_media_enabled%22%3Atrue%2C%22responsive_web_media_download_video_enabled%22%3Afalse%2C%22responsive_web_enhance_cards_enabled%22%3Afalse%7D",
		CallLimit: 500,
		DefaultVariables: map[string]interface{}{
			"tweetId":                "1701892872574996627",
			"count":                  20,
			"includePromotedContent": false,
		},
		DefaultFeatures: map[string]interface{}{
			"responsive_web_graphql_exclude_directive_enabled":                        true,
			"verified_phone_label_enabled":                                            false,
			"creator_subscriptions_tweet_preview_api_enabled":                         true,
			"responsive_web_graphql_timeline_navigation_enabled":                      true,
			"responsive_web_graphql_skip_user_profile_image_extensions_enabled":       false,
			"c9s_tweet_anatomy_moderator_badge_enabled":                               true,
			"tweetypie_unmention_optimization_enabled":                                true,
			"responsive_web_edit_tweet_api_enabled":                                   true,
			"graphql_is_translatable_rweb_tweet_is_translatable_enabled":              true,
			"view_counts_everywhere_api_enabled":                                      true,
			"longform_notetweets_consumption_enabled":                                 true,
			"responsive_web_twitter_article_tweet_consumption_enabled":                false,
			"tweet_awards_web_tipping_enabled":                                        false,
			"freedom_of_speech_not_reach_fetch_enabled":                               true,
			"standardized_nudges_misinfo":                                             true,
			"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
			"rweb_video_timestamps_enabled":                                           true,
			"longform_notetweets_rich_text_read_enabled":                              true,
			"longform_notetweets_inline_media_enabled":                                true,
			"responsive_web_media_download_video_enabled":                             false,
			"responsive_web_enhance_cards_enabled":                                    false,
		},
		AdditionalParams: nil,
	}
	XBaseEndpointFavoriters = &XBaseEndpoint{
		BaseURL:   "https://twitter.com/i/api/graphql/E-ZTxvWWIkmOKwYdNTEefg/Favoriters?variables=%7B%22tweetId%22%3A%221701892872574996627%22%2C%22count%22%3A20%2C%22includePromotedContent%22%3Atrue%7D&features=%7B%22responsive_web_graphql_exclude_directive_enabled%22%3Atrue%2C%22verified_phone_label_enabled%22%3Afalse%2C%22creator_subscriptions_tweet_preview_api_enabled%22%3Atrue%2C%22responsive_web_graphql_timeline_navigation_enabled%22%3Atrue%2C%22responsive_web_graphql_skip_user_profile_image_extensions_enabled%22%3Afalse%2C%22c9s_tweet_anatomy_moderator_badge_enabled%22%3Atrue%2C%22tweetypie_unmention_optimization_enabled%22%3Atrue%2C%22responsive_web_edit_tweet_api_enabled%22%3Atrue%2C%22graphql_is_translatable_rweb_tweet_is_translatable_enabled%22%3Atrue%2C%22view_counts_everywhere_api_enabled%22%3Atrue%2C%22longform_notetweets_consumption_enabled%22%3Atrue%2C%22responsive_web_twitter_article_tweet_consumption_enabled%22%3Afalse%2C%22tweet_awards_web_tipping_enabled%22%3Afalse%2C%22freedom_of_speech_not_reach_fetch_enabled%22%3Atrue%2C%22standardized_nudges_misinfo%22%3Atrue%2C%22tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled%22%3Atrue%2C%22rweb_video_timestamps_enabled%22%3Atrue%2C%22longform_notetweets_rich_text_read_enabled%22%3Atrue%2C%22longform_notetweets_inline_media_enabled%22%3Atrue%2C%22responsive_web_media_download_video_enabled%22%3Afalse%2C%22responsive_web_enhance_cards_enabled%22%3Afalse%7D",
		CallLimit: 500,
		DefaultVariables: map[string]interface{}{
			"tweetId":                "1701892872574996627",
			"count":                  20,
			"includePromotedContent": true,
		},
		DefaultFeatures: map[string]interface{}{
			"responsive_web_graphql_exclude_directive_enabled":                        true,
			"verified_phone_label_enabled":                                            false,
			"creator_subscriptions_tweet_preview_api_enabled":                         true,
			"responsive_web_graphql_timeline_navigation_enabled":                      true,
			"responsive_web_graphql_skip_user_profile_image_extensions_enabled":       false,
			"c9s_tweet_anatomy_moderator_badge_enabled":                               true,
			"tweetypie_unmention_optimization_enabled":                                true,
			"responsive_web_edit_tweet_api_enabled":                                   true,
			"graphql_is_translatable_rweb_tweet_is_translatable_enabled":              true,
			"view_counts_everywhere_api_enabled":                                      true,
			"longform_notetweets_consumption_enabled":                                 true,
			"responsive_web_twitter_article_tweet_consumption_enabled":                false,
			"tweet_awards_web_tipping_enabled":                                        false,
			"freedom_of_speech_not_reach_fetch_enabled":                               true,
			"standardized_nudges_misinfo":                                             true,
			"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
			"rweb_video_timestamps_enabled":                                           true,
			"longform_notetweets_rich_text_read_enabled":                              true,
			"longform_notetweets_inline_media_enabled":                                true,
			"responsive_web_media_download_video_enabled":                             false,
			"responsive_web_enhance_cards_enabled":                                    false,
		},
		AdditionalParams: nil,
	}
	XBaseEndpointFollowing = &XBaseEndpoint{
		BaseURL:   "https://twitter.com/i/api/graphql/0yD6Eiv23DKXRDU9VxlG2A/Following?variables=%7B%22userId%22%3A%221415522287126671363%22%2C%22count%22%3A20%2C%22includePromotedContent%22%3Afalse%7D&features=%7B%22responsive_web_graphql_exclude_directive_enabled%22%3Atrue%2C%22verified_phone_label_enabled%22%3Afalse%2C%22creator_subscriptions_tweet_preview_api_enabled%22%3Atrue%2C%22responsive_web_graphql_timeline_navigation_enabled%22%3Atrue%2C%22responsive_web_graphql_skip_user_profile_image_extensions_enabled%22%3Afalse%2C%22c9s_tweet_anatomy_moderator_badge_enabled%22%3Atrue%2C%22tweetypie_unmention_optimization_enabled%22%3Atrue%2C%22responsive_web_edit_tweet_api_enabled%22%3Atrue%2C%22graphql_is_translatable_rweb_tweet_is_translatable_enabled%22%3Atrue%2C%22view_counts_everywhere_api_enabled%22%3Atrue%2C%22longform_notetweets_consumption_enabled%22%3Atrue%2C%22responsive_web_twitter_article_tweet_consumption_enabled%22%3Afalse%2C%22tweet_awards_web_tipping_enabled%22%3Afalse%2C%22freedom_of_speech_not_reach_fetch_enabled%22%3Atrue%2C%22standardized_nudges_misinfo%22%3Atrue%2C%22tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled%22%3Atrue%2C%22rweb_video_timestamps_enabled%22%3Atrue%2C%22longform_notetweets_rich_text_read_enabled%22%3Atrue%2C%22longform_notetweets_inline_media_enabled%22%3Atrue%2C%22responsive_web_media_download_video_enabled%22%3Afalse%2C%22responsive_web_enhance_cards_enabled%22%3Afalse%7D",
		CallLimit: 500,
		DefaultVariables: map[string]interface{}{
			"userId":                 "1415522287126671363",
			"count":                  20,
			"includePromotedContent": false,
		},
		DefaultFeatures: map[string]interface{}{
			"responsive_web_graphql_exclude_directive_enabled":                        true,
			"verified_phone_label_enabled":                                            false,
			"creator_subscriptions_tweet_preview_api_enabled":                         true,
			"responsive_web_graphql_timeline_navigation_enabled":                      true,
			"responsive_web_graphql_skip_user_profile_image_extensions_enabled":       false,
			"c9s_tweet_anatomy_moderator_badge_enabled":                               true,
			"tweetypie_unmention_optimization_enabled":                                true,
			"responsive_web_edit_tweet_api_enabled":                                   true,
			"graphql_is_translatable_rweb_tweet_is_translatable_enabled":              true,
			"view_counts_everywhere_api_enabled":                                      true,
			"longform_notetweets_consumption_enabled":                                 true,
			"responsive_web_twitter_article_tweet_consumption_enabled":                false,
			"tweet_awards_web_tipping_enabled":                                        false,
			"freedom_of_speech_not_reach_fetch_enabled":                               true,
			"standardized_nudges_misinfo":                                             true,
			"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
			"rweb_video_timestamps_enabled":                                           true,
			"longform_notetweets_rich_text_read_enabled":                              true,
			"longform_notetweets_inline_media_enabled":                                true,
			"responsive_web_media_download_video_enabled":                             false,
			"responsive_web_enhance_cards_enabled":                                    false,
		},
		AdditionalParams: nil,
	}

	XEndpointKeySearchTimeline XEndpointKey = "search-timeline"
	XEndpointKeyRetweeters     XEndpointKey = "retweeters"
	XEndpointKeyFavoriters     XEndpointKey = "favoriters"
	XEndpointKeyFollowing      XEndpointKey = "following"

	DefaultEndpoints = func() map[XEndpointKey]*XEndpoint {
		return map[XEndpointKey]*XEndpoint{
			XEndpointKeySearchTimeline: {XBaseEndpoint: XBaseEndpointSearchTimeline},
			XEndpointKeyRetweeters:     {XBaseEndpoint: XBaseEndpointRetweeters},
			XEndpointKeyFavoriters:     {XBaseEndpoint: XBaseEndpointFavoriters},
			XEndpointKeyFollowing:      {XBaseEndpoint: XBaseEndpointFollowing},
		}
	}
)

type (
	XEndpointKey  string
	XBaseEndpoint struct {
		BaseURL          string
		CallLimit        int64
		DefaultVariables map[string]interface{}
		DefaultFeatures  map[string]interface{}
		AdditionalParams map[string]map[string]interface{}
	}
)

type XEndpoint struct {
	*XBaseEndpoint
	Status      string
	ErrorReason string
	Remaining   int64
	Reset       int64
	Pending     int64
}

type XClient struct {
	Account     *XAccount
	HttpClient  *http.Client
	Status      string
	FlowToken   string
	ErrorReason string

	Endpoints map[XEndpointKey]*XEndpoint
}

func (client *XClient) FetchLimits(ctx context.Context) {
	wg := &sync.WaitGroup{}

	for _, endpoint := range client.Endpoints {
		wg.Add(1)
		go func(ctx context.Context, endpoint *XEndpoint) {
			defer wg.Done()

			req, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL, nil)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
			req.Header.Set("X-Twitter-Active-User", "yes")
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
			for _, c := range client.HttpClient.Jar.Cookies(req.URL) {
				if c.Name == "ct0" {
					req.Header.Set("X-Csrf-Token", c.Value)
					continue
				}
				if c.Name == "auth_token" {
					req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
					continue
				}
			}
			resp, err := client.HttpClient.Do(req)
			defer func() { _ = resp.Body.Close() }()

			newRemaining, err := strconv.ParseInt(resp.Header.Get("X-Rate-Limit-Remaining"), 10, 64)
			if err != nil {
				endpoint.Status = XEndpointStatusErrored
				endpoint.ErrorReason = err.Error()
				return
			}
			endpoint.Remaining = newRemaining

			newReset, err := strconv.ParseInt(resp.Header.Get("X-Rate-Limit-Reset"), 10, 64)
			if err != nil {
				endpoint.Status = XEndpointStatusErrored
				endpoint.ErrorReason = err.Error()
				return
			}
			endpoint.Reset = newReset

			switch resp.StatusCode {
			case http.StatusOK:
				bodyBz, err := io.ReadAll(resp.Body)
				if err != nil {
					endpoint.Status = XEndpointStatusErrored
					endpoint.ErrorReason = err.Error()
					return
				}

				var respBody struct {
					Errors []struct {
						Message string `json:"message"`
						Name    string `json:"name"`
						Kind    string `json:"kind"`
						Code    int    `json:"code"`
					} `json:"errors"`
				}

				err = json.Unmarshal(bodyBz, &respBody)
				if err == nil && len(respBody.Errors) > 0 {
					for _, e := range respBody.Errors {
						if e.Name == "AuthorizationError" {
							endpoint.Status = XEndpointStatusForbidden
							return
						}
					}
				}
				endpoint.Status = XEndpointStatusOk

			case http.StatusForbidden:
				endpoint.Status = XEndpointStatusForbidden
				return

			default:
				bodyBz, _ := io.ReadAll(resp.Body)
				log.Println(string(bodyBz))
				endpoint.Status = XEndpointStatusErrored
				endpoint.ErrorReason = fmt.Sprintf("unexpected response code %d", resp.StatusCode)
			}
		}(ctx, endpoint)
	}

	wg.Wait()
}
