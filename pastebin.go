package pastebin

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
)

const (
	LoginApiUrl    = "https://pastebin.com/api/api_login.php"
	PostApiUrl     = "https://pastebin.com/api/api_post.php"
	RawApiUrl      = "https://pastebin.com/api/api_raw.php"
	ScrapingApiUrl = "https://scrape.pastebin.com/api_scraping.php"

	// RawUrlPrefix is not part of the supported API, but can still be used to fetch raw pastes.
	// See GetRawPaste
	RawUrlPrefix = "https://pastebin.com/raw"
)

var (
	ErrNotAuthenticated = errors.New("must be authenticated to perform this action")
)

type Client struct {
	username        string
	password        string
	developerApiKey string
	sessionKey      string
}

// NewClient creates a new Client and authenticates said client before returning if the username parameter is passed.
//
// Note that the only thing you can do without providing a username and a password is creating a new guest paste.
func NewClient(username, password, developerApiKey string) (*Client, error) {
	client := &Client{
		username:        username,
		password:        password,
		developerApiKey: developerApiKey,
	}
	if len(username) > 0 {
		return client, client.login()
	}
	return client, nil
}

func (c *Client) login() error {
	responseBody, err := c.doPastebinRequest(LoginApiUrl, url.Values{
		"api_user_name":     {c.username},
		"api_user_password": {c.password},
		"api_dev_key":       {c.developerApiKey},
	}, false)
	if err != nil {
		return err
	}
	c.sessionKey = string(responseBody)
	return nil
}

// CreatePaste creates a new paste and returns the paste key
// If the client was only provided with a developer API key, a guest paste will be created.
// You can get the URL by simply appending the output key to "https://pastebin.com/"
func (c *Client) CreatePaste(request *CreatePasteRequest) (string, error) {
	if request.Visibility == VisibilityPrivate && len(c.sessionKey) == 0 {
		return "", ErrNotAuthenticated
	}
	expirationField := ExpirationNever
	if len(request.Expiration) > 0 {
		expirationField = request.Expiration
	}
	responseBody, err := c.doPastebinRequest(PostApiUrl, url.Values{
		"api_option":            {"paste"},
		"api_user_key":          {c.sessionKey},
		"api_dev_key":           {c.developerApiKey},
		"api_paste_name":        {request.Title},
		"api_paste_code":        {request.Code},
		"api_paste_format":      {request.Syntax},
		"api_paste_expire_date": {string(expirationField)},
		"api_paste_private":     {fmt.Sprintf("%d", request.Visibility)},
	}, true)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(string(responseBody), "https://pastebin.com/"), nil
}

// DeletePaste removes a paste that belongs to the authenticated user
func (c *Client) DeletePaste(pasteKey string) error {
	if len(c.sessionKey) == 0 {
		return ErrNotAuthenticated
	}
	_, err := c.doPastebinRequest(RawApiUrl, url.Values{
		"api_option":    {"delete"},
		"api_user_key":  {c.sessionKey},
		"api_dev_key":   {c.developerApiKey},
		"api_paste_key": {pasteKey},
	}, true)
	return err
}

// ListUserPastes retrieves a list of pastes from the authenticated user
func (c *Client) ListUserPastes() ([]*Paste, error) {
	if len(c.sessionKey) == 0 {
		return nil, ErrNotAuthenticated
	}
	responseBody, err := c.doPastebinRequest(PostApiUrl, url.Values{
		"api_option":        {"list"},
		"api_user_key":      {c.sessionKey},
		"api_dev_key":       {c.developerApiKey},
		"api_results_limit": {"100"},
	}, true)
	if err != nil {
		return nil, err
	}
	var xmlPastes xmlPastes
	err = xml.Unmarshal([]byte(fmt.Sprintf("<pastes>%s</pastes>", string(responseBody))), &xmlPastes)
	if err != nil {
		return nil, err
	}
	var pastes []*Paste
	for _, xmlPaste := range xmlPastes.Pastes {
		pastes = append(pastes, xmlPaste.ToPaste(c.username))
	}
	return pastes, nil
}

// GetRawUserPaste retrieves the content of a paste from the authenticated user
// Unlike GetRawPaste, this function can only get the content of a paste that belongs to the authenticated user,
// even if the paste is public.
func (c *Client) GetRawUserPaste(pasteKey string) (string, error) {
	if len(c.sessionKey) == 0 {
		return "", ErrNotAuthenticated
	}
	responseBody, err := c.doPastebinRequest(RawApiUrl, url.Values{
		"api_option":    {"show_paste"},
		"api_user_key":  {c.sessionKey},
		"api_dev_key":   {c.developerApiKey},
		"api_paste_key": {pasteKey},
	}, true)
	if err != nil {
		return "", err
	}
	return string(responseBody), nil
}

// GetRecentPastes retrieves the most recent pastes using Pastebin's scraping API
func (c *Client) GetRecentPastes() (string, error) {
	if len(c.sessionKey) == 0 {
		return "", ErrNotAuthenticated
	}
	responseBody, err := c.doPastebinRequest(ScrapingApiUrl, url.Values{
		"api_option":    {"show_paste"},
		"api_user_key":  {c.sessionKey},
		"api_dev_key":   {c.developerApiKey},
	}, true)
	if err != nil {
		return "", err
	}
	var jsonPastes jsonPastes
	err = json.Unmarshal([]byte(fmt.Sprintf("{\"pastes\":%s}", string(responseBody))), &jsonPastes)
	if err != nil {
		return "", err
	}
	var pastes []*Paste
	for _, jsonPaste := range jsonPastes.Pastes {
		pastes = append(pastes, jsonPaste.ToPaste())
	}
	return string(responseBody), nil
}

// doPastebinRequest performs an HTTP request to the provided Pastebin API URL with the given fields
// If reAuthenticateOnInvalidSessionKey is true, will automatically attempt to re-login on invalid api_user_key
func (c *Client) doPastebinRequest(apiUrl string, fields url.Values, reAuthenticateOnInvalidSessionKey bool) ([]byte, error) {
	client := getHttpClient()
	request, err := http.NewRequest("POST", apiUrl, bytes.NewBuffer([]byte(fields.Encode())))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, errors.New(response.Status)
	}
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if reAuthenticateOnInvalidSessionKey && string(body) == "Bad API request, invalid api_user_key" {
		fmt.Println("re-authenticating due to invalid api_user_key")
		err = c.login()
		if err != nil {
			return nil, fmt.Errorf("failed to re-authenticate on invalid api_user_key response: %s", err.Error())
		}
		// Retry the request one more time
		return c.doPastebinRequest(apiUrl, fields, false)
	}
	if strings.HasPrefix(string(body), "Bad API request") {
		return nil, errors.New(string(body))
	}
	return body, nil
}

// GetRawPaste retrieves the content of a paste by using the raw endpoint (https://pastebin.com/raw/{pasteKey})
// This does not require authentication, but only works with public and unlisted pastes.
//
// WARNING: Using this excessively could lead to your IP being blocked.
// You may want to use the Client variants of this function.
func GetRawPaste(pasteKey string) (string, error) {
	client := getHttpClient()
	response, err := client.Get(fmt.Sprintf("%s/%s", RawUrlPrefix, pasteKey))
	if err != nil {
		return "", err
	}
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != 200 || strings.HasPrefix(string(body), "Bad API request") {
		return "", errors.New(string(body))
	}
	return string(body), nil
}
