package seerrApi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type HTTPError struct {
	StatusCode int
	Status     string
	Method     string
	URL        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("failed to %s %s: %s", e.Method, e.URL, e.Status)
}

type Client struct {
	httpClient *http.Client
	baseUrlUrl *url.URL
	baseUrl    string
	apiKey     string
}

func NewClient(hostUrl, apiKey, hardcodedEndpoint string) (*Client, error) {
	seerrHostUrl, err := url.Parse(hostUrl)
	if err != nil {
		return nil, err
	}
	if seerrHostUrl.Scheme == "" || seerrHostUrl.Host == "" {
		return nil, errors.New("missing scheme/host")
	}

	seerrHostUrl = seerrHostUrl.JoinPath("api", "v1", "/", hardcodedEndpoint)
	return &Client{
		baseUrlUrl: seerrHostUrl,
		baseUrl:    seerrHostUrl.String(),
		apiKey:     apiKey,
		httpClient: &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil, // $HTTP_PROXY etc. ignored
				MaxIdleConns:          http.DefaultTransport.(*http.Transport).MaxIdleConns,
				IdleConnTimeout:       http.DefaultTransport.(*http.Transport).IdleConnTimeout,
				TLSHandshakeTimeout:   http.DefaultTransport.(*http.Transport).TLSHandshakeTimeout,
				ExpectContinueTimeout: http.DefaultTransport.(*http.Transport).ExpectContinueTimeout,
				ResponseHeaderTimeout: 10 * time.Second,
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: time.Minute}).DialContext,
				ForceAttemptHTTP2:     false,
			},
		},
	}, nil
}

func (c *Client) do(method string, endpoint string, queryParams url.Values, reqBody any, respBody any) error {
	var finalUrl string
	if queryParams == nil {
		if endpoint == "" {
			finalUrl = c.baseUrl
		} else {
			finalUrl = c.baseUrl + endpoint
		}
	} else {
		var u *url.URL
		if endpoint == "" {
			u = &(*c.baseUrlUrl)
		} else {
			u = c.baseUrlUrl.JoinPath(endpoint)
		}
		u.RawQuery = queryParams.Encode()

		finalUrl = u.String()
	}

	var pReqBody io.Reader = nil
	var jsonBuf bytes.Buffer
	if reqBody != nil {
		jsonEnc := json.NewEncoder(&jsonBuf)
		jsonEnc.SetEscapeHTML(false)
		if err := jsonEnc.Encode(reqBody); err != nil {
			return fmt.Errorf("failed to serialise request body to JSON for %s: %w", finalUrl, err)
		}
		pReqBody = &jsonBuf
	}

	req, err := http.NewRequest(method, finalUrl, pReqBody)
	if err != nil {
		return fmt.Errorf("failed to create %s request for %s: %w", method, finalUrl, err)
	}
	req.Header.Set("Connection", "keep-alive")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if respBody != nil {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("X-Api-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= 300 {
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Method:     method,
			URL:        finalUrl,
		}
	}

	if respBody != nil {
		if ptr, ok := respBody.(*string); !ok {
			err = json.NewDecoder(resp.Body).Decode(respBody)
		} else {
			var all []byte
			all, err = io.ReadAll(resp.Body)
			if err == nil {
				*ptr = string(all)
			}
		}

		if err != nil {
			return fmt.Errorf("failed to decode JSON response from %s: %w", finalUrl, err)
		}
	}

	return nil
}

func (c *Client) Delete(endpoint string, queryParams url.Values, reqBody any) error {
	return c.do(http.MethodDelete, endpoint, queryParams, reqBody, nil)
}

func (c *Client) Get(endpoint string, queryParams url.Values, respBody any) error {
	return c.do(http.MethodGet, endpoint, queryParams, nil, respBody)
}

func (c *Client) put(endpoint string, queryParams url.Values, reqBody any, respBody any) error {
	return c.do(http.MethodPut, endpoint, queryParams, reqBody, respBody)
}

func (c *Client) Post(endpoint string, queryParams url.Values, reqBody any, respBody any) error {
	return c.do(http.MethodPost, endpoint, queryParams, reqBody, respBody)
}
