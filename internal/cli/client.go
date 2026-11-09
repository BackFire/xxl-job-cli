package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 16 << 20

type client struct {
	settings settings
	http     *http.Client
}

type page struct {
	Data     []map[string]any `json:"data"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

func newClient(s settings) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &client{settings: s, http: &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *client) login() error {
	path := "/auth/doLogin"
	if legacyLogin(c.settings.APIVersion) {
		path = "/login"
	}
	form := url.Values{"userName": {c.settings.Username}, "password": {c.settings.Password}}
	body, err := c.post(path, form)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if _, err := responseData(body); err != nil {
		return fmt.Errorf("login: %s", c.redact(err.Error()))
	}
	if len(c.http.Jar.Cookies(c.cookieURL())) == 0 {
		return errors.New("login succeeded without a session cookie")
	}
	return nil
}

func (c *client) post(path string, form url.Values) ([]byte, error) {
	u := *c.settings.Endpoint
	u.Path = strings.TrimRight(u.Path, "/") + path
	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request %s returned HTTP %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("response exceeds 16 MiB")
	}
	return body, nil
}

func decodeJSON(data []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return ensureEOF(dec)
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected extra JSON value")
		}
		return err
	}
	return nil
}

func responseData(body []byte) (json.RawMessage, error) {
	var response struct {
		Code    *int            `json:"code"`
		Message string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
		Content json.RawMessage `json:"content"`
	}
	if err := decodeJSON(body, &response); err != nil {
		return nil, err
	}
	if response.Code == nil {
		return nil, errors.New("response has no status code")
	}
	if *response.Code != 200 {
		return nil, fmt.Errorf("server rejected request (code %d): %s", *response.Code, response.Message)
	}
	if len(response.Data) > 0 {
		return response.Data, nil
	}
	if len(response.Content) > 0 {
		return response.Content, nil
	}
	return json.RawMessage("null"), nil
}

func (c *client) request(path string, form url.Values) (any, error) {
	body, err := c.post(path, form)
	if err != nil {
		return nil, err
	}
	data, err := responseData(body)
	if err != nil {
		return nil, errors.New(c.redact(err.Error()))
	}
	var value any
	if err := decodeJSON(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (c *client) list(path string, form url.Values, number, size int) (page, error) {
	if number < 1 || size < 1 || size > 1000 {
		return page{}, errors.New("page must be >= 1 and page-size must be between 1 and 1000")
	}
	if number > int(^uint(0)>>1)/size {
		return page{}, errors.New("page offset is too large")
	}
	offset := (number - 1) * size
	if !modernAPI(c.settings.APIVersion) {
		form.Set("start", fmt.Sprint(offset))
		form.Set("length", fmt.Sprint(size))
	} else {
		form.Set("offset", fmt.Sprint(offset))
		form.Set("pagesize", fmt.Sprint(size))
	}
	body, err := c.post(path, form)
	if err != nil {
		return page{}, err
	}
	if modernAPI(c.settings.APIVersion) {
		body, err = responseData(body)
		if err != nil {
			return page{}, errors.New(c.redact(err.Error()))
		}
	}
	var result struct {
		Data         []map[string]any `json:"data"`
		Total        *int             `json:"total"`
		RecordsTotal *int             `json:"recordsTotal"`
	}
	if err := decodeJSON(body, &result); err != nil {
		return page{}, err
	}
	total := result.Total
	if !modernAPI(c.settings.APIVersion) {
		total = result.RecordsTotal
	}
	if total == nil || *total < 0 || result.Data == nil {
		return page{}, errors.New("unexpected page response")
	}
	return page{Data: result.Data, Total: *total, Page: number, PageSize: size}, nil
}

func (c *client) redact(message string) string {
	if c.settings.Password != "" {
		message = strings.ReplaceAll(message, c.settings.Password, "[REDACTED]")
	}
	for _, cookie := range c.http.Jar.Cookies(c.cookieURL()) {
		if cookie.Value != "" {
			message = strings.ReplaceAll(message, cookie.Value, "[REDACTED]")
		}
	}
	return message
}

func (c *client) cookieURL() *url.URL {
	u := *c.settings.Endpoint
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	return &u
}

func (c *client) findJob(group, id int) (map[string]any, error) {
	const size = 100
	for number := 1; ; number++ {
		p, err := c.list("/jobinfo/pageList", jobFilters(group, -1, "", "", ""), number, size)
		if err != nil {
			return nil, err
		}
		for _, job := range p.Data {
			if n, ok := job["id"].(json.Number); ok && n.String() == fmt.Sprint(id) {
				return job, nil
			}
		}
		if len(p.Data) == 0 || number*size >= p.Total {
			return nil, fmt.Errorf("job %d not found in group %d", id, group)
		}
	}
}
