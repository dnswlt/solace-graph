// Package swcat provides a client for the swcat catalog HTTP API.
package swcat

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	catalogpb "github.com/dnswlt/solace-graph/internal/catalog/pb"
)

// Client talks to a swcat server over its HTTP API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient returns a Client for the swcat server at baseURL (e.g.
// "http://localhost:9191").
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Entities retrieves all catalog entities via GET {baseURL}/catalog/entities.
func (c *Client) Entities() ([]*catalogpb.Entity, error) {
	endpoint := c.baseURL + "/catalog/entities"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %v", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %v", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %v", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: unexpected status %s: %s", endpoint, resp.Status, bytes.TrimSpace(body))
	}

	var listResp catalogpb.ListEntitiesResponse
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("parsing response from %s: %v", endpoint, err)
	}
	return listResp.GetEntities(), nil
}
