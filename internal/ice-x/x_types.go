package ice_x

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hiendaovinh/toolkit/pkg/arr"
	"github.com/hiendaovinh/toolkit/pkg/errorx"
)

type Reply struct {
	TweetID         string    `json:"tweet_id"`
	UserID          string    `json:"user_id"`
	Text            string    `json:"text"`
	NormalizedText  string    `json:"normalized_text"`
	CreatedAt       time.Time `json:"created_at"`
	Hashtags        []string  `json:"hashtags"`
	LoweredHashtags []string  `json:"lowered_hashtags"`
	Symbols         []string  `json:"symbols"`
	LoweredSymbols  []string  `json:"lowered_symbols"`
	Sort            int64     `json:"sort"`
}

type Quote struct {
	TweetID         string    `json:"tweet_id"`
	UserID          string    `json:"user_id"`
	Text            string    `json:"text"`
	NormalizedText  string    `json:"normalized_text"`
	CreatedAt       time.Time `json:"created_at"`
	Hashtags        []string  `json:"hashtags"`
	LoweredHashtags []string  `json:"lowered_hashtags"`
	Symbols         []string  `json:"symbols"`
	LoweredSymbols  []string  `json:"lowered_symbols"`
	Sort            int64     `json:"sort"`
}

type Retweet struct {
	TweetID string `json:"tweet_id"`
	UserID  string `json:"user_id"`
	Sort    int64  `json:"sort"`
}

type Like struct {
	TweetID string `json:"tweet_id"`
	UserID  string `json:"user_id"`
	Sort    int64  `json:"sort"`
}

type Following struct {
	TargetID   string `json:"target_id"`
	UserID     string `json:"user_id"`
	Name       string `json:"name"`
	ScreenName string `json:"screen_name"`
}

type StatusStat struct {
	UserID         string    `json:"user_id"`
	UserScreenName string    `json:"user_screen_name"`
	UserName       string    `json:"user_name"`
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	IsQuoteStatus  bool      `json:"is_quote_status"`
	ViewCount      int64     `json:"view_count"`
	QuoteCount     int64     `json:"quote_count"`
	ReplyCount     int64     `json:"reply_count"`
	RetweetCount   int64     `json:"retweet_count"`
	FavoriteCount  int64     `json:"favorite_count"`
	TweetUrl       string    `json:"tweet_url"`
}

func (manager *XManager) nextClient(endpointKey XEndpointKey) (*XClient, func(statusCode int, header http.Header, body []byte)) {
	manager.rwMtx.Lock()
	defer manager.rwMtx.Unlock()

	perm := rand.Perm(len(manager.clients))
	keys := make([]string, 0, len(manager.clients))
	for username := range manager.clients {
		keys = append(keys, username)
	}

	var selectedClient *XClient
	var selectedEndpoint *XEndpoint
	for _, j := range perm {
		client := manager.clients[keys[j]]
		if client.Status != XClientStatusLoggedIn {
			continue
		}

		endpoint, ok := client.Endpoints[endpointKey]
		if !ok || endpoint.Status != XEndpointStatusOk {
			continue
		}

		if endpoint.Reset < time.Now().Unix() {
			if endpoint.Pending >= endpoint.CallLimit {
				continue
			}
		} else {
			if endpoint.Pending >= endpoint.Remaining {
				continue
			}
		}

		endpoint.Pending++
		selectedClient = client
		selectedEndpoint = endpoint
	}

	releaseFn := func(statusCode int, header http.Header, body []byte) {
		manager.rwMtx.Lock()
		defer manager.rwMtx.Unlock()

		if selectedEndpoint == nil {
			return
		}

		// release by decrease pending counter
		selectedEndpoint.Pending--
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			selectedEndpoint.Status = XEndpointStatusForbidden
			selectedEndpoint.ErrorReason = "response code unauthorized or forbidden"
			return
		}

		var respBody struct {
			Errors []Error `json:"errors"`
		}

		err := json.Unmarshal(body, &respBody)
		if err == nil && len(respBody.Errors) > 0 {
			for _, e := range respBody.Errors {
				if e.Name == "AuthorizationError" {
					selectedEndpoint.Status = XEndpointStatusForbidden
					selectedEndpoint.ErrorReason = e.Message
					return
				}
			}
		}

		newRemaining, _ := strconv.ParseInt(header.Get("X-Rate-Limit-Remaining"), 10, 64)
		newReset, _ := strconv.ParseInt(header.Get("X-Rate-Limit-Reset"), 10, 64)

		if newReset == selectedEndpoint.Reset {
			// two requests is in the same timeframe
			// if later request somehow acquired lock first, newRemaining > client.remaining, and we should ignore this case
			if newRemaining < selectedEndpoint.Remaining {
				selectedEndpoint.Remaining = newRemaining
			}
		} else if newReset > selectedEndpoint.Reset {
			selectedEndpoint.Reset = newReset
			selectedEndpoint.Remaining = newRemaining
		}
	}

	return selectedClient, releaseFn
}

func (manager *XManager) Replies(ctx context.Context, tweetID string, cursor string) ([]Reply, string, error) {
	var statusCode int
	headers := http.Header{}
	var bodyBz []byte

	client, releaseFn := manager.nextClient(XEndpointKeySearchTimeline)
	defer releaseFn(statusCode, headers, bodyBz)

	if client == nil {
		return nil, "", errorx.Wrap(fmt.Errorf("service is busy"), errorx.RateLimiting)
	}

	req, _ := http.NewRequest("GET", XBaseEndpointSearchTimeline.BaseURL, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
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

	variables := make(map[string]interface{})
	for k, v := range XBaseEndpointSearchTimeline.DefaultVariables {
		variables[k] = v
	}

	variables["rawQuery"] = fmt.Sprintf("filter:replies conversation_id:%s", tweetID)
	variables["count"] = 20
	variables["product"] = "Latest"
	variables["querySource"] = "tdqt"
	if cursor != "" {
		variables["cursor"] = cursor
	}

	variablesBz, _ := json.Marshal(variables)
	featuresBz, _ := json.Marshal(XBaseEndpointSearchTimeline.DefaultFeatures)
	values := req.URL.Query()
	values.Set("variables", string(variablesBz))
	values.Set("features", string(featuresBz))
	req.URL.RawQuery = values.Encode()
	req = req.WithContext(ctx)

	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	statusCode = resp.StatusCode
	for headerName, headerValues := range resp.Header {
		for _, headerValue := range headerValues {
			headers.Add(headerName, headerValue)
		}
	}

	bodyBz, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	var respObj SearchTimelineResponse
	err = json.Unmarshal(bodyBz, &respObj)
	if err != nil {
		return nil, "", err
	}

	if len(respObj.Errors) > 0 {
		return nil, "", fmt.Errorf("server returns error: %s", strings.Join(
			arr.ArrMap(respObj.Errors, func(err Error) string { return err.Message }),
			";",
		))
	}

	replies := make([]Reply, 0)
	nextCursor := ""
	mainInstructionEntryLength := 0

	for _, instruction := range respObj.Data.SearchByRawQuery.SearchTimeline.Timeline.Instructions {
		if instruction.Type == "TimelineReplaceEntry" && instruction.Entry != nil {
			if instruction.Entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[instruction.Entry.Content.CursorType] {
				nextCursor = instruction.Entry.Content.Value
			}
			continue
		}

		if instruction.Type != "TimelineAddEntries" {
			continue
		}

		for _, entry := range instruction.Entries {
			// itemContent is not null if entry is either main or cursor
			if entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[entry.Content.CursorType] {
				nextCursor = entry.Content.Value
				continue
			}

			// defensive
			if entry.Content.ItemContent == nil {
				continue
			}

			mainInstructionEntryLength++
			if entry.Content.ItemContent.TweetResults.Result.Legacy.InReplyToStatusIDStr != tweetID {
				continue
			}

			tweetResult := entry.Content.ItemContent.TweetResults.Result
			parts := make([]string, 0, len(tweetResult.Legacy.Entities.URLs)*2+1)
			lastPartIndex := 0

			for _, u := range tweetResult.Legacy.Entities.URLs {
				parts = append(parts, tweetResult.Legacy.FullText[lastPartIndex:u.Indices[0]])
				parts = append(parts, u.DisplayURL)
				lastPartIndex = u.Indices[1]
			}
			parts = append(parts, tweetResult.Legacy.FullText[lastPartIndex:])
			normalizedText := strings.Join(parts, "")

			replies = append(replies, Reply{
				TweetID:         tweetID,
				UserID:          tweetResult.Core.UserResults.Result.RestID,
				Text:            tweetResult.Legacy.FullText,
				NormalizedText:  normalizedText,
				CreatedAt:       tweetResult.Legacy.CreatedAt,
				Hashtags:        arr.ArrMap(tweetResult.Legacy.Entities.Hashtags, func(v Hashtag) string { return v.Text }),
				LoweredHashtags: arr.ArrMap(tweetResult.Legacy.Entities.Hashtags, func(v Hashtag) string { return strings.ToLower(v.Text) }),
				Symbols:         arr.ArrMap(tweetResult.Legacy.Entities.Symbols, func(v Symbol) string { return v.Text }),
				LoweredSymbols:  arr.ArrMap(tweetResult.Legacy.Entities.Symbols, func(v Symbol) string { return strings.ToLower(v.Text) }),
				Sort:            entry.SortIndex,
			})
		}
	}

	if mainInstructionEntryLength == 0 {
		nextCursor = ""
	}

	return replies, nextCursor, nil
}

func (manager *XManager) Quotes(ctx context.Context, tweetID string, cursor string) ([]Quote, string, error) {
	var statusCode int
	headers := http.Header{}
	var bodyBz []byte

	client, releaseFn := manager.nextClient(XEndpointKeySearchTimeline)
	defer releaseFn(statusCode, headers, bodyBz)

	if client == nil {
		return nil, "", errorx.Wrap(fmt.Errorf("service is busy"), errorx.RateLimiting)
	}

	req, _ := http.NewRequest("GET", XBaseEndpointSearchTimeline.BaseURL, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
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

	variables := make(map[string]interface{})
	for k, v := range XBaseEndpointSearchTimeline.DefaultVariables {
		variables[k] = v
	}

	variables["rawQuery"] = fmt.Sprintf("quoted_tweet_id:%s", tweetID)
	variables["count"] = 20
	variables["product"] = "Latest"
	variables["querySource"] = "tdqt"
	if cursor != "" {
		variables["cursor"] = cursor
	}

	variablesBz, _ := json.Marshal(variables)
	featuresBz, _ := json.Marshal(XBaseEndpointSearchTimeline.DefaultFeatures)
	values := req.URL.Query()
	values.Set("variables", string(variablesBz))
	values.Set("features", string(featuresBz))
	req.URL.RawQuery = values.Encode()
	req = req.WithContext(ctx)

	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	statusCode = resp.StatusCode
	for headerName, headerValues := range resp.Header {
		for _, headerValue := range headerValues {
			headers.Add(headerName, headerValue)
		}
	}

	bodyBz, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	var respObj SearchTimelineResponse
	err = json.Unmarshal(bodyBz, &respObj)
	if err != nil {
		return nil, "", err
	}

	if len(respObj.Errors) > 0 {
		return nil, "", fmt.Errorf("server returns error: %s", strings.Join(
			arr.ArrMap(respObj.Errors, func(err Error) string { return err.Message }),
			";",
		))
	}

	quotes := make([]Quote, 0)
	nextCursor := ""
	mainInstructionEntryLength := 0

	for _, instruction := range respObj.Data.SearchByRawQuery.SearchTimeline.Timeline.Instructions {
		if instruction.Type == "TimelineReplaceEntry" && instruction.Entry != nil {
			if instruction.Entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[instruction.Entry.Content.CursorType] {
				nextCursor = instruction.Entry.Content.Value
			}
			continue
		}

		if instruction.Type != "TimelineAddEntries" {
			continue
		}

		for _, entry := range instruction.Entries {
			// itemContent is not null if entry is either main or cursor
			if entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[entry.Content.CursorType] {
				nextCursor = entry.Content.Value
				continue
			}

			if entry.Content.ItemContent == nil {
				continue
			}

			mainInstructionEntryLength++
			if entry.Content.ItemContent.TweetResults.Result.Legacy.QuotedStatusIDStr != tweetID {
				continue
			}

			tweetResult := entry.Content.ItemContent.TweetResults.Result
			parts := make([]string, 0, len(tweetResult.Legacy.Entities.URLs)*2+1)
			lastPartIndex := 0

			for _, u := range tweetResult.Legacy.Entities.URLs {
				parts = append(parts, tweetResult.Legacy.FullText[lastPartIndex:u.Indices[0]])
				parts = append(parts, u.DisplayURL)
				lastPartIndex = u.Indices[1]
			}
			parts = append(parts, tweetResult.Legacy.FullText[lastPartIndex:])
			normalizedText := strings.Join(parts, "")

			quotes = append(quotes, Quote{
				TweetID:         tweetID,
				UserID:          tweetResult.Core.UserResults.Result.RestID,
				Text:            tweetResult.Legacy.FullText,
				NormalizedText:  normalizedText,
				CreatedAt:       tweetResult.Legacy.CreatedAt,
				Hashtags:        arr.ArrMap(tweetResult.Legacy.Entities.Hashtags, func(v Hashtag) string { return v.Text }),
				LoweredHashtags: arr.ArrMap(tweetResult.Legacy.Entities.Hashtags, func(v Hashtag) string { return strings.ToLower(v.Text) }),
				Symbols:         arr.ArrMap(tweetResult.Legacy.Entities.Symbols, func(v Symbol) string { return v.Text }),
				LoweredSymbols:  arr.ArrMap(tweetResult.Legacy.Entities.Symbols, func(v Symbol) string { return strings.ToLower(v.Text) }),
				Sort:            entry.SortIndex,
			})
		}
	}

	if mainInstructionEntryLength == 0 {
		nextCursor = ""
	}

	return quotes, nextCursor, nil
}

func (manager *XManager) Retweets(ctx context.Context, tweetID string, cursor string) ([]Retweet, string, error) {
	var statusCode int
	headers := http.Header{}
	var bodyBz []byte

	client, releaseFn := manager.nextClient(XEndpointKeyRetweeters)
	defer releaseFn(statusCode, headers, bodyBz)

	if client == nil {
		return nil, "", errorx.Wrap(fmt.Errorf("service is busy"), errorx.RateLimiting)
	}

	req, _ := http.NewRequest("GET", XBaseEndpointRetweeters.BaseURL, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
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

	variables := make(map[string]interface{})
	for k, v := range XBaseEndpointRetweeters.DefaultVariables {
		variables[k] = v
	}

	variables["tweetId"] = tweetID
	variables["count"] = 100
	variables["includePromotedContent"] = true
	if cursor != "" {
		variables["cursor"] = cursor
	}

	variablesBz, _ := json.Marshal(variables)
	featuresBz, _ := json.Marshal(XBaseEndpointRetweeters.DefaultFeatures)
	values := req.URL.Query()
	values.Set("variables", string(variablesBz))
	values.Set("features", string(featuresBz))
	req.URL.RawQuery = values.Encode()
	req = req.WithContext(ctx)

	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	statusCode = resp.StatusCode
	for headerName, headerValues := range resp.Header {
		for _, headerValue := range headerValues {
			headers.Add(headerName, headerValue)
		}
	}

	bodyBz, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	var respObj RetweetersResponse
	err = json.Unmarshal(bodyBz, &respObj)
	if err != nil {
		return nil, "", err
	}

	if len(respObj.Errors) > 0 {
		return nil, "", fmt.Errorf("server returns error: %s", strings.Join(
			arr.ArrMap(respObj.Errors, func(err Error) string { return err.Message }),
			";",
		))
	}

	retweeters := make([]Retweet, 0)
	nextCursor := ""

	for _, instruction := range respObj.Data.RetweetersTimeline.Timeline.Instructions {
		if instruction.Type != "TimelineAddEntries" {
			continue
		}

		for _, entry := range instruction.Entries {
			// itemContent is not null if entry is either main or cursor
			if entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[entry.Content.CursorType] {
				nextCursor = entry.Content.Value
				continue
			}

			if entry.Content.ItemContent == nil {
				continue
			}

			userID := entry.Content.ItemContent.UserResults.Result.RestID
			if userID == "" {
				if matches := regexUserEntryID.FindStringSubmatch(entry.EntryID); len(matches) == 2 {
					userID = matches[1]
				}
			}

			retweeters = append(retweeters, Retweet{
				TweetID: tweetID,
				UserID:  userID,
				Sort:    entry.SortIndex,
			})
		}
	}

	if len(retweeters) == 0 {
		nextCursor = ""
	}

	return retweeters, nextCursor, nil
}

func (manager *XManager) Likes(ctx context.Context, tweetID string, cursor string) ([]Like, string, error) {
	var statusCode int
	headers := http.Header{}
	var bodyBz []byte

	client, releaseFn := manager.nextClient(XEndpointKeyFavoriters)
	defer releaseFn(statusCode, headers, bodyBz)

	if client == nil {
		return nil, "", errorx.Wrap(fmt.Errorf("service is busy"), errorx.RateLimiting)
	}

	req, _ := http.NewRequest("GET", XBaseEndpointFavoriters.BaseURL, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
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

	variables := make(map[string]interface{})
	for k, v := range XBaseEndpointFavoriters.DefaultVariables {
		variables[k] = v
	}

	variables["tweetId"] = tweetID
	variables["count"] = 100
	variables["includePromotedContent"] = true
	if cursor != "" {
		variables["cursor"] = cursor
	}

	variablesBz, _ := json.Marshal(variables)
	featuresBz, _ := json.Marshal(XBaseEndpointFavoriters.DefaultFeatures)
	values := req.URL.Query()
	values.Set("variables", string(variablesBz))
	values.Set("features", string(featuresBz))
	req.URL.RawQuery = values.Encode()
	req = req.WithContext(ctx)

	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	statusCode = resp.StatusCode
	for headerName, headerValues := range resp.Header {
		for _, headerValue := range headerValues {
			headers.Add(headerName, headerValue)
		}
	}

	bodyBz, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	var respObj FavoritersResponse
	err = json.Unmarshal(bodyBz, &respObj)
	if err != nil {
		return nil, "", err
	}

	if len(respObj.Errors) > 0 {
		return nil, "", fmt.Errorf("server returns error: %s", strings.Join(
			arr.ArrMap(respObj.Errors, func(err Error) string { return err.Message }),
			";",
		))
	}

	favoriters := make([]Like, 0)
	nextCursor := ""

	for _, instruction := range respObj.Data.FavoritersTimeline.Timeline.Instructions {
		if instruction.Type != "TimelineAddEntries" {
			continue
		}

		for _, entry := range instruction.Entries {
			// itemContent is not null if entry is either main or cursor
			if entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[entry.Content.CursorType] {
				nextCursor = entry.Content.Value
				continue
			}

			if entry.Content.ItemContent == nil {
				continue
			}

			userID := entry.Content.ItemContent.UserResults.Result.RestID
			if userID == "" {
				if matches := regexUserEntryID.FindStringSubmatch(entry.EntryID); len(matches) == 2 {
					userID = matches[1]
				}
			}

			favoriters = append(favoriters, Like{
				TweetID: tweetID,
				UserID:  userID,
				Sort:    entry.SortIndex,
			})
		}
	}

	if len(favoriters) == 0 {
		nextCursor = ""
	}

	return favoriters, nextCursor, nil
}

func (manager *XManager) Following(ctx context.Context, targetID string, cursor string) ([]Following, string, error) {
	var statusCode int
	headers := http.Header{}
	var bodyBz []byte

	client, releaseFn := manager.nextClient(XEndpointKeyFollowing)
	defer releaseFn(statusCode, headers, bodyBz)

	if client == nil {
		return nil, "", errorx.Wrap(fmt.Errorf("service is busy"), errorx.RateLimiting)
	}

	req, _ := http.NewRequest("GET", XBaseEndpointFollowing.BaseURL, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
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

	variables := make(map[string]interface{})
	for k, v := range XBaseEndpointFollowing.DefaultVariables {
		variables[k] = v
	}

	variables["userId"] = targetID
	variables["count"] = 20
	variables["includePromotedContent"] = false
	if cursor != "" {
		variables["cursor"] = cursor
	}

	variablesBz, _ := json.Marshal(variables)
	featuresBz, _ := json.Marshal(XBaseEndpointFollowing.DefaultFeatures)
	values := req.URL.Query()
	values.Set("variables", string(variablesBz))
	values.Set("features", string(featuresBz))
	req.URL.RawQuery = values.Encode()
	req = req.WithContext(ctx)

	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	statusCode = resp.StatusCode
	for headerName, headerValues := range resp.Header {
		for _, headerValue := range headerValues {
			headers.Add(headerName, headerValue)
		}
	}

	bodyBz, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	var respObj FollowingResponse
	err = json.Unmarshal(bodyBz, &respObj)
	if err != nil {
		return nil, "", err
	}

	if len(respObj.Errors) > 0 {
		return nil, "", fmt.Errorf("server returns error: %s", strings.Join(
			arr.ArrMap(respObj.Errors, func(err Error) string { return err.Message }),
			";",
		))
	}

	followings := make([]Following, 0)
	nextCursor := ""

	for _, instruction := range respObj.Data.User.Result.Timeline.Timeline.Instructions {
		if instruction.Type != "TimelineAddEntries" {
			continue
		}

		for _, entry := range instruction.Entries {
			// itemContent is not null if entry is either main or cursor
			if entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[entry.Content.CursorType] {
				nextCursor = entry.Content.Value
				continue
			}

			if entry.Content.ItemContent == nil {
				continue
			}

			userID := entry.Content.ItemContent.UserResults.Result.RestID
			if userID == "" {
				if matches := regexUserEntryID.FindStringSubmatch(entry.EntryID); len(matches) == 2 {
					userID = matches[1]
				}
			}

			followings = append(followings, Following{
				TargetID:   targetID,
				UserID:     userID,
				Name:       entry.Content.ItemContent.UserResults.Result.Legacy.Name,
				ScreenName: entry.Content.ItemContent.UserResults.Result.Legacy.ScreenName,
			})
		}
	}

	if len(followings) == 0 {
		nextCursor = ""
	}

	return followings, nextCursor, nil
}

func (manager *XManager) StatusesByScreenName(ctx context.Context, screenName string, cursor string) ([]StatusStat, string, error) {
	var statusCode int
	headers := http.Header{}
	var bodyBz []byte

	client, releaseFn := manager.nextClient(XEndpointKeySearchTimeline)
	defer releaseFn(statusCode, headers, bodyBz)

	if client == nil {
		return nil, "", errorx.Wrap(fmt.Errorf("service is busy"), errorx.RateLimiting)
	}

	req, _ := http.NewRequest("GET", XBaseEndpointSearchTimeline.BaseURL, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
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

	variables := make(map[string]interface{})
	for k, v := range XBaseEndpointSearchTimeline.DefaultVariables {
		variables[k] = v
	}

	variables["rawQuery"] = fmt.Sprintf("(from:%s) -filter:replies", screenName)
	variables["count"] = 20
	variables["product"] = "Latest"
	variables["querySource"] = "typed_query"
	if cursor != "" {
		variables["cursor"] = cursor
	}

	variablesBz, _ := json.Marshal(variables)
	featuresBz, _ := json.Marshal(XBaseEndpointSearchTimeline.DefaultFeatures)
	values := req.URL.Query()
	values.Set("variables", string(variablesBz))
	values.Set("features", string(featuresBz))
	req.URL.RawQuery = values.Encode()
	req = req.WithContext(ctx)

	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	statusCode = resp.StatusCode
	for headerName, headerValues := range resp.Header {
		for _, headerValue := range headerValues {
			headers.Add(headerName, headerValue)
		}
	}

	bodyBz, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	var respObj SearchTimelineResponse
	err = json.Unmarshal(bodyBz, &respObj)
	if err != nil {
		return nil, "", err
	}

	if len(respObj.Errors) > 0 {
		return nil, "", fmt.Errorf("server returns error: %s", strings.Join(
			arr.ArrMap(respObj.Errors, func(err Error) string { return err.Message }),
			";",
		))
	}

	statuses := make([]StatusStat, 0)
	nextCursor := ""
	mainInstructionEntryLength := 0

	for _, instruction := range respObj.Data.SearchByRawQuery.SearchTimeline.Timeline.Instructions {
		if instruction.Type == "TimelineReplaceEntry" && instruction.Entry != nil {
			if instruction.Entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[instruction.Entry.Content.CursorType] {
				nextCursor = instruction.Entry.Content.Value
			}
			continue
		}

		if instruction.Type != "TimelineAddEntries" {
			continue
		}

		for _, entry := range instruction.Entries {
			// itemContent is not null if entry is either main or cursor
			if entry.Content.EntryType == "TimelineTimelineCursor" && cursorTypes[entry.Content.CursorType] {
				nextCursor = entry.Content.Value
				continue
			}

			if entry.Content.ItemContent == nil {
				continue
			}

			mainInstructionEntryLength++
			if entry.Content.ItemContent.TweetResults.Result.Core.UserResults.Result.Legacy.ScreenName != screenName {
				continue
			}

			tweetResult := entry.Content.ItemContent.TweetResults.Result

			var tweetUrl string

			entryID := strings.Split(entry.EntryID, "-")
			if len(entryID) > 1 {
				tweetUrl = fmt.Sprintf("https://twitter.com/%s/status/%s", screenName, entryID[1])
			}

			statuses = append(statuses, StatusStat{
				UserID:         tweetResult.Core.UserResults.Result.RestID,
				UserScreenName: tweetResult.Core.UserResults.Result.Legacy.ScreenName,
				UserName:       tweetResult.Core.UserResults.Result.Legacy.Name,
				ID:             tweetResult.RestID,
				CreatedAt:      tweetResult.Legacy.CreatedAt,
				IsQuoteStatus:  tweetResult.Legacy.IsQuoteStatus,
				ViewCount:      tweetResult.Views.Count,
				FavoriteCount:  tweetResult.Legacy.FavoriteCount,
				QuoteCount:     tweetResult.Legacy.QuoteCount,
				ReplyCount:     tweetResult.Legacy.ReplyCount,
				RetweetCount:   tweetResult.Legacy.RetweetCount,
				TweetUrl:       tweetUrl,
			})
		}
	}

	if mainInstructionEntryLength == 0 {
		nextCursor = ""
	}

	return statuses, nextCursor, nil
}

type UserMention struct {
	IDStr string `json:"id_str"`
}

type Hashtag struct {
	Text string `json:"text"`
}

type Symbol struct {
	Text string `json:"text"`
}

type URL struct {
	DisplayURL  string `json:"display_url"`
	ExpandedURL string `json:"expanded_url"`
	URL         string `json:"url"`
	Indices     [2]int `json:"indices"`
}

type UserResults struct {
	Result struct {
		RestID string          `json:"rest_id"`
		Legacy TweetUserLegacy `json:"legacy"`
	} `json:"result"`
}

type TweetUserLegacy struct {
	Name       string `json:"name"`
	ScreenName string `json:"screen_name"`
}

type TweetResults struct {
	Result struct {
		RestID string `json:"rest_id"`
		Core   struct {
			UserResults UserResults `json:"user_results"`
		} `json:"core"`
		Legacy TweetResultLegacy `json:"legacy"`
		Views  TweetResultViews  `json:"views"`
	} `json:"result"`
}

type TweetResultViews struct {
	Count int64 `json:"count"`
}

func (obj *TweetResultViews) UnmarshalJSON(data []byte) error {
	type Alias TweetResultViews
	aux := &struct {
		*Alias
		Count string `json:"count"`
	}{
		Alias: (*Alias)(obj),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if aux.Count != "" {
		count, err := strconv.ParseInt(aux.Count, 10, 64)
		if err != nil {
			return fmt.Errorf("cannot unmarshal %s into an int64", aux.Count)
		}
		obj.Count = count
	}

	return nil
}

type TweetResultLegacy struct {
	CreatedAt time.Time `json:"created_at"`
	Entities  struct {
		UserMentions []UserMention `json:"user_mention"`
		Hashtags     []Hashtag     `json:"hashtags"`
		Symbols      []Symbol      `json:"symbols"`
		URLs         []URL         `json:"urls"`
	} `json:"entities"`
	FullText             string `json:"full_text"`
	InReplyToStatusIDStr string `json:"in_reply_to_status_id_str"`
	QuotedStatusIDStr    string `json:"quoted_status_id_str"`

	FavoriteCount int64 `json:"favorite_count"`
	QuoteCount    int64 `json:"quote_count"`
	ReplyCount    int64 `json:"reply_count"`
	RetweetCount  int64 `json:"retweet_count"`
	IsQuoteStatus bool  `json:"is_quote_status"`
}

func (obj *TweetResultLegacy) UnmarshalJSON(data []byte) error {
	type Alias TweetResultLegacy
	aux := &struct {
		*Alias
		CreatedAt string `json:"created_at"`
	}{
		Alias: (*Alias)(obj),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	createdAt, err := time.Parse(time.RubyDate, aux.CreatedAt)
	if err != nil {
		return fmt.Errorf("cannot unmarshal %s into a time.Time (%s)", aux.CreatedAt, time.RubyDate)
	}
	obj.CreatedAt = createdAt

	return nil
}

type Error struct {
	Message string `json:"message"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Code    int    `json:"code"`
}

// ========= Responses

// SearchTimelineResponse is response from SearchTimeline API
// count: 20
// rate limit 50 per 15 minutes
type SearchTimelineResponse struct {
	Data struct {
		SearchByRawQuery struct {
			SearchTimeline struct {
				Timeline struct {
					Instructions []SearchTimelineInstruction `json:"instructions"`
				} `json:"timeline"`
			} `json:"search_timeline"`
		} `json:"search_by_raw_query"`
	} `json:"data"`
	Errors []Error `json:"errors"`
}

// RetweetersResponse is response from Retweeters API
// count: 100
// rate limit 500 per 15 minutes
type RetweetersResponse struct {
	Data struct {
		RetweetersTimeline struct {
			Timeline struct {
				Instructions []RetweetersInstruction `json:"instructions"`
			} `json:"timeline"`
		} `json:"retweeters_timeline"`
	} `json:"data"`
	Errors []Error `json:"errors"`
}

// FavoritersResponse is response from Favoriters API
// count: 100
// rate limit 500 per 15 minutes
type FavoritersResponse struct {
	Data struct {
		FavoritersTimeline struct {
			Timeline struct {
				Instructions []FavoritersInstruction `json:"instructions"`
			} `json:"timeline"`
		} `json:"favoriters_timeline"`
	} `json:"data"`
	Errors []Error `json:"errors"`
}

type FollowingResponse struct {
	Data struct {
		User struct {
			Result struct {
				Timeline struct {
					Timeline struct {
						Instructions []FollowingInstruction `json:"instructions"`
					} `json:"timeline"`
				} `json:"timeline"`
			} `json:"result"`
		} `json:"user"`
	} `json:"data"`
	Errors []Error `json:"errors"`
}

// ========= Instructions

type Instruction[T any] struct {
	Type    string `json:"type"`
	Entries []T    `json:"entries"`
}

type (
	SearchTimelineInstruction struct {
		Instruction[SearchTimelineEntry]
		Entry *InstructionEntry `json:"entry"`
	}
	RetweetersInstruction Instruction[RetweetersEntry]
	FavoritersInstruction Instruction[FavoritersEntry]
	FollowingInstruction  Instruction[FollowingEntry]
)

// ========= Entries

type Entry[T any] struct {
	EntryID   string `json:"entryId"`
	SortIndex int64  `json:"sortIndex"`
	Content   T      `json:"content"`
}
type (
	InstructionEntry struct {
		Content InstructionEntryContent `json:"content"`
	}
	SearchTimelineEntry Entry[SearchTimelineEntryContent]
	RetweetersEntry     Entry[RetweetersEntryContent]
	FavoritersEntry     Entry[FavoritersEntryContent]
	FollowingEntry      Entry[FollowingEntryContent]
)

func (obj *SearchTimelineEntry) UnmarshalJSON(data []byte) error {
	type Alias SearchTimelineEntry
	aux := &struct {
		*Alias
		SortIndex string `json:"sortIndex"`
	}{
		Alias: (*Alias)(obj),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	sortIndex, err := strconv.ParseInt(aux.SortIndex, 10, 64)
	if err != nil {
		return fmt.Errorf("cannot unmarshal %s into an int64", aux.SortIndex)
	}
	obj.SortIndex = sortIndex

	return nil
}

func (obj *RetweetersEntry) UnmarshalJSON(data []byte) error {
	type Alias RetweetersEntry
	aux := &struct {
		*Alias
		SortIndex string `json:"sortIndex"`
	}{
		Alias: (*Alias)(obj),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	sortIndex, err := strconv.ParseInt(aux.SortIndex, 10, 64)
	if err != nil {
		return fmt.Errorf("cannot unmarshal %s into an int64", aux.SortIndex)
	}
	obj.SortIndex = sortIndex

	return nil
}

func (obj *FavoritersEntry) UnmarshalJSON(data []byte) error {
	type Alias FavoritersEntry
	aux := &struct {
		*Alias
		SortIndex string `json:"sortIndex"`
	}{
		Alias: (*Alias)(obj),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	sortIndex, err := strconv.ParseInt(aux.SortIndex, 10, 64)
	if err != nil {
		return fmt.Errorf("cannot unmarshal %s into an int64", aux.SortIndex)
	}
	obj.SortIndex = sortIndex

	return nil
}

func (obj *FollowingEntry) UnmarshalJSON(data []byte) error {
	type Alias FollowingEntry
	aux := &struct {
		*Alias
		SortIndex string `json:"sortIndex"`
	}{
		Alias: (*Alias)(obj),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	sortIndex, err := strconv.ParseInt(aux.SortIndex, 10, 64)
	if err != nil {
		return fmt.Errorf("cannot unmarshal %s into an int64", aux.SortIndex)
	}
	obj.SortIndex = sortIndex

	return nil
}

// ========= EntryContent

type EmptyEntryContent struct {
	EntryType  string `json:"entryType"`
	CursorType string `json:"cursorType"`
	Value      string `json:"value"`
}

type EntryContent[T any] struct {
	EmptyEntryContent
	ItemContent *T `json:"itemContent"` // nullable
}

type (
	InstructionEntryContent    EmptyEntryContent
	SearchTimelineEntryContent EntryContent[SearchTimelineEntryItemContent]
	RetweetersEntryContent     EntryContent[RetweetersEntryItemContent]
	FavoritersEntryContent     EntryContent[FavoritersEntryItemContent]
	FollowingEntryContent      EntryContent[FollowingEntryItemContent]
)

// ========= EntryContentItem

type SearchTimelineEntryItemContent struct {
	ItemType     string       `json:"itemType"`
	TweetResults TweetResults `json:"tweet_results"`
}

type RetweetersEntryItemContent struct {
	ItemType    string      `json:"itemType"`
	UserResults UserResults `json:"user_results"`
}

type FavoritersEntryItemContent struct {
	ItemType    string      `json:"itemType"`
	UserResults UserResults `json:"user_results"`
}

type FollowingEntryItemContent struct {
	ItemType    string      `json:"itemType"`
	UserResults UserResults `json:"user_results"`
}
