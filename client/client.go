// Package client provides an authenticated Slack client wrapper.
package client

import (
	"fmt"
	"net/http"

	slackapi "github.com/slack-go/slack"
)

// Client wraps *slack.Client with token metadata for raw API calls.
type Client struct {
	*slackapi.Client
	token  string
	cookie string
}

// Token returns the raw Slack token.
func (c *Client) Token() string { return c.token }

// Cookie returns the session cookie (empty for bot/user tokens).
func (c *Client) Cookie() string { return c.cookie }

// HTTPDo executes a raw HTTP request with authentication headers set.
func (c *Client) HTTPDo(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.cookie != "" {
		req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.cookie))
	}
	return http.DefaultClient.Do(req)
}

// New creates an authenticated Slack client with optional cookie support.
// Extra slack.Option values (e.g. slackapi.OptionAPIURL for testing) are
// appended after the cookie transport option.
func New(token, cookie string, opts ...slackapi.Option) *Client {
	var allOpts []slackapi.Option
	if cookie != "" {
		allOpts = append(allOpts, slackapi.OptionHTTPClient(
			&cookieHTTPClient{cookie: cookie},
		))
	}
	allOpts = append(allOpts, opts...)
	return &Client{
		Client: slackapi.New(token, allOpts...),
		token:  token,
		cookie: cookie,
	}
}

// cookieHTTPClient injects the d= cookie on every request.
type cookieHTTPClient struct {
	cookie string
}

func (c *cookieHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.cookie))
	return http.DefaultClient.Do(req)
}
