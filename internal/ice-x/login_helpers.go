package ice_x

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

func tryLogin(ctx context.Context, account *XAccount) *XClient {
	if account.Cookies != "" {
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
		return &XClient{
			Account:     account,
			HttpClient:  httpClient,
			Status:      XClientStatusLoggedIn,
			ErrorReason: "",
			Endpoints:   DefaultEndpoints(),
		}
	}

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}

	var err error
	err = initGuessToken(ctx, httpClient)
	if err != nil {
		err2 := initGuessToken2(ctx, httpClient)
		if err2 != nil {
			return &XClient{
				Account:     account,
				HttpClient:  nil,
				Status:      XClientStatusErrored,
				ErrorReason: fmt.Sprintf("%v;%v", err.Error(), err2.Error()),
				Endpoints:   nil,
			}
		}
	}

	var flowToken string
	flowToken, err = startLoginFlow(ctx, httpClient)
	if err != nil {
		return &XClient{
			Account:     account,
			HttpClient:  nil,
			Status:      XClientStatusErrored,
			ErrorReason: err.Error(),
			Endpoints:   nil,
		}
	}

	flowToken, err = loginJsInstrumentationSubtask(ctx, httpClient, flowToken)
	if err != nil {
		return &XClient{
			Account:     account,
			HttpClient:  nil,
			Status:      XClientStatusErrored,
			ErrorReason: err.Error(),
			Endpoints:   nil,
		}
	}

	flowToken, err = loginEnterUserIdentifierSSO(ctx, httpClient, flowToken, account.Username)
	if err != nil {
		return &XClient{
			Account:     account,
			HttpClient:  nil,
			Status:      XClientStatusErrored,
			ErrorReason: err.Error(),
			Endpoints:   nil,
		}
	}

	flowToken, err = loginEnterPassword(ctx, httpClient, flowToken, account.Password)
	if err != nil {
		return &XClient{
			Account:     account,
			HttpClient:  nil,
			Status:      XClientStatusErrored,
			ErrorReason: err.Error(),
			Endpoints:   nil,
		}
	}

	flowToken, requireConfirmEmail, requireConfirmationCode, err := accountDuplicationCheck(ctx, httpClient, flowToken)
	if err != nil {
		return &XClient{
			Account:     account,
			HttpClient:  nil,
			Status:      XClientStatusErrored,
			ErrorReason: err.Error(),
			Endpoints:   nil,
		}
	}

	// ugly if, but who cares ?
	if requireConfirmEmail {
		if account.Email == "" {
			return &XClient{
				Account:     account,
				HttpClient:  httpClient,
				Status:      XClientStatusWaitingForEmail,
				FlowToken:   flowToken,
				ErrorReason: "",
				Endpoints:   nil,
			}
		}

		flowToken, err = confirmEmail(ctx, httpClient, flowToken, account.Email)
		if err != nil {
			return &XClient{
				Account:     account,
				HttpClient:  nil,
				Status:      XClientStatusErrored,
				ErrorReason: err.Error(),
				Endpoints:   nil,
			}
		}
	} else if requireConfirmationCode {
		if !account.AutoEmailConfirmationCode || account.Email == "" || account.EmailPassword == "" || !strings.HasSuffix(account.Email, "@hotmail.com") {
			return &XClient{
				Account:     account,
				HttpClient:  nil,
				Status:      XClientStatusWaitingForConfirmationCode,
				FlowToken:   flowToken,
				ErrorReason: "",
				Endpoints:   nil,
			}
		}

		var code string
		code, err = getConfirmationCode(ctx, account.Email, account.EmailPassword)
		if err != nil {
			return &XClient{
				Account:     account,
				HttpClient:  nil,
				Status:      XClientStatusErrored,
				ErrorReason: err.Error(),
				Endpoints:   nil,
			}
		}

		flowToken, err = confirmationCode(ctx, httpClient, flowToken, code)
		if err != nil {
			return &XClient{
				Account:     account,
				HttpClient:  nil,
				Status:      XClientStatusErrored,
				ErrorReason: err.Error(),
				Endpoints:   nil,
			}
		}
	}

	return &XClient{
		Account:     account,
		HttpClient:  httpClient,
		Status:      XClientStatusLoggedIn,
		ErrorReason: "",
		Endpoints:   DefaultEndpoints(),
	}
}

func initGuessToken(ctx context.Context, httpClient *http.Client) error {
	req, err := http.NewRequest(http.MethodGet, "https://twitter.com/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "curl/8.5.0")
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	matches := regexp.MustCompile(`gt=(\d+)`).FindSubmatch(bodyBz)
	if len(matches) != 2 {
		return fmt.Errorf("cannot extract guess token, len(matches) = %d", len(matches))
	}

	cookies := httpClient.Jar.Cookies(req.URL)
	cookies = append(cookies, &http.Cookie{
		Name:   "gt",
		Value:  string(matches[1]),
		Path:   "/",
		Domain: ".twitter.com",
		MaxAge: 10800,
		Secure: true,
	})
	httpClient.Jar.SetCookies(req.URL, cookies)

	return nil
}

func initGuessToken2(ctx context.Context, httpClient *http.Client) error {
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/guest/activate.json", nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "curl/8.5.0")
	req.Header.Set("Content-Type", "x-www-form-urlencoded")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var respBody struct {
		GuestToken string `json:"guest_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return err
	}

	cookies := httpClient.Jar.Cookies(req.URL)
	cookies = append(cookies, &http.Cookie{
		Name:   "gt",
		Value:  respBody.GuestToken,
		Path:   "/",
		Domain: ".twitter.com",
		MaxAge: 10800,
		Secure: true,
	})
	httpClient.Jar.SetCookies(req.URL, cookies)

	return nil
}

func startLoginFlow(ctx context.Context, httpClient *http.Client) (string, error) {
	reqBodyBz := []byte(`{"input_flow_data":{"flow_context":{"debug_overrides":{},"start_location":{"location":"unknown"}}},"subtask_versions":{"action_list":2,"alert_dialog":1,"app_download_cta":1,"check_logged_in_account":1,"choice_selection":3,"contacts_live_sync_permission_prompt":0,"cta":7,"email_verification":2,"end_flow":1,"enter_date":1,"enter_email":2,"enter_password":5,"enter_phone":2,"enter_recaptcha":1,"enter_text":5,"enter_username":2,"generic_urt":3,"in_app_notification":1,"interest_picker":3,"js_instrumentation":1,"menu_dialog":1,"notifications_permission_prompt":2,"open_account":2,"open_home_timeline":1,"open_link":1,"phone_verification":4,"privacy_options":1,"security_key":3,"select_avatar":4,"select_banner":2,"settings_list":7,"show_code":1,"sign_up":2,"sign_up_review":4,"tweet_selection_urt":1,"update_users":1,"upload_media":1,"user_recommendations_list":4,"user_recommendations_urt":1,"wait_spinner":3,"web_modal":1}}`)
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json?flow_name=login", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", err
	}

	return respBody.FlowToken, nil
}

func loginJsInstrumentationSubtask(ctx context.Context, httpClient *http.Client, flowToken string) (string, error) {
	reqBodyBz, _ := json.Marshal(map[string]interface{}{
		"flow_token": flowToken,
		"subtask_inputs": []map[string]interface{}{
			{
				"subtask_id": "LoginJsInstrumentationSubtask",
				"js_instrumentation": map[string]interface{}{
					"response": "{}",
					"link":     "next_link",
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", err
	}

	return respBody.FlowToken, nil
}

func loginEnterUserIdentifierSSO(ctx context.Context, httpClient *http.Client, flowToken string, username string) (string, error) {
	reqBodyBz, _ := json.Marshal(map[string]interface{}{
		"flow_token": flowToken,
		"subtask_inputs": []map[string]interface{}{
			{
				"subtask_id": "LoginEnterUserIdentifierSSO",
				"settings_list": map[string]interface{}{
					"setting_responses": []map[string]interface{}{
						{
							"key": "user_identifier",
							"response_data": map[string]interface{}{
								"text_data": map[string]interface{}{
									"result": username,
								},
							},
						},
					},
					"link": "next_link",
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", err
	}

	return respBody.FlowToken, nil
}

func loginEnterPassword(ctx context.Context, httpClient *http.Client, flowToken string, password string) (string, error) {
	reqBodyBz, _ := json.Marshal(map[string]interface{}{
		"flow_token": flowToken,
		"subtask_inputs": []map[string]interface{}{
			{
				"subtask_id": "LoginEnterPassword",
				"enter_password": map[string]interface{}{
					"password": password,
					"link":     "next_link",
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", err
	}

	return respBody.FlowToken, nil
}

func accountDuplicationCheck(ctx context.Context, httpClient *http.Client, flowToken string) (string, bool, bool, error) {
	reqBodyBz, _ := json.Marshal(map[string]interface{}{
		"flow_token": flowToken,
		"subtask_inputs": []map[string]interface{}{
			{
				"subtask_id": "AccountDuplicationCheck",
				"check_logged_in_account": map[string]interface{}{
					"link": "AccountDuplicationCheck_false",
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", false, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", false, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", false, false, fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, false, err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
		Subtasks  []struct {
			SubtaskID string                 `json:"subtask_id"`
			EnterText map[string]interface{} `json:"enter_text"`
		} `json:"subtasks"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", false, false, err
	}

	requireConfirmEmail := false
	requireConfirmationCode := false

	for _, subtask := range respBody.Subtasks {
		if subtask.SubtaskID == "LoginAcid" {
			if subtask.EnterText == nil {
				continue
			}

			keyboardType, ok := subtask.EnterText["keyboard_type"].(string)
			if ok && keyboardType == "email" {
				requireConfirmEmail = true
			}

			hintText, _ := subtask.EnterText["hint_text"].(string)
			if strings.ToLower(hintText) == "confirmation code" {
				requireConfirmationCode = true
			}
		}
	}

	return respBody.FlowToken, requireConfirmEmail, requireConfirmationCode, nil
}

func confirmEmail(ctx context.Context, httpClient *http.Client, flowToken string, email string) (string, error) {
	reqBodyBz, _ := json.Marshal(map[string]interface{}{
		"flow_token": flowToken,
		"subtask_inputs": []map[string]interface{}{
			{
				"subtask_id": "LoginAcid",
				"enter_text": map[string]interface{}{
					"text": email,
					"link": "next_link",
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", err
	}

	return respBody.FlowToken, nil
}

func confirmationCode(ctx context.Context, httpClient *http.Client, flowToken string, code string) (string, error) {
	reqBodyBz, _ := json.Marshal(map[string]interface{}{
		"flow_token": flowToken,
		"subtask_inputs": []map[string]interface{}{
			{
				"subtask_id": "LoginAcid",
				"enter_text": map[string]interface{}{
					"text": code,
					"link": "next_link",
				},
			},
		},
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.twitter.com/1.1/onboarding/task.json", bytes.NewReader(reqBodyBz))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	cookies := httpClient.Jar.Cookies(req.URL)
	for _, c := range cookies {
		if c.Name == "gt" {
			req.Header.Set("X-Guest-Token", c.Value)
			break
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", defaultBearerToken))
	req = req.WithContext(ctx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response code %d", resp.StatusCode)
	}

	bodyBz, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var respBody struct {
		FlowToken string `json:"flow_token"`
	}
	err = json.Unmarshal(bodyBz, &respBody)
	if err != nil {
		return "", err
	}

	return respBody.FlowToken, nil
}

func getConfirmationCode(ctx context.Context, email string, password string) (string, error) {
	notBefore := time.Now().Add(-time.Minute)

	options := &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}

	c, err := imapclient.DialTLS("imap-mail.outlook.com:993", options)
	if err != nil {
		return "", fmt.Errorf("failed to dial: %v", err)
	}

	err = c.Login(email, password).Wait()
	if err != nil {
		return "", fmt.Errorf("failed to login: %s", err)
	}

	defer func() {
		_ = c.Logout().Wait()
		_ = c.Close()
	}()

	_, err = c.Select("INBOX", nil).Wait()
	if err != nil {
		return "", fmt.Errorf("failed to select inbox: %v", err)
	}

	attempt := 0
	maxAttempt := 10
	sleepTime := 5 * time.Second

	timer := time.NewTimer(0)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			code, err := tryGetConfirmationCode(c, notBefore)
			if err == nil {
				return code, nil
			}
			attempt++
			if attempt >= maxAttempt {
				return "", fmt.Errorf("max attepmts exceed")
			}
			timer.Reset(sleepTime)
		}
	}
}

func tryGetConfirmationCode(c *imapclient.Client, notBefore time.Time) (string, error) {
	searchResult, err := c.Search(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: "info@x.com"}},
	}, nil).Wait()
	if err != nil {
		return "", err
	}

	if len(searchResult.AllNums()) == 0 {
		return "", fmt.Errorf("no matching mail found")
	}

	fetchOptions := &imap.FetchOptions{
		Flags:    true,
		Envelope: true,
	}
	fetchCmd := c.Fetch(searchResult.All, fetchOptions)
	defer func() { _ = fetchCmd.Close() }()

	messages, err := fetchCmd.Collect()
	if err != nil {
		return "", err
	}

	if len(messages) == 0 {
		return "", fmt.Errorf("no matching mail found")
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Envelope.Date.Before(notBefore) {
			continue
		}

		subject := msg.Envelope.Subject
		matches := regexp.MustCompile(`Your Twitter confirmation code is (.+)`).FindStringSubmatch(subject)
		if len(matches) != 2 {
			matches = regexp.MustCompile(`(.+) is your Twitter verification code`).FindStringSubmatch(subject)
			if len(matches) != 2 {
				continue
			}
		}
		return matches[1], nil
	}

	return "", fmt.Errorf("no matching mail found")
}
