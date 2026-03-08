package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GraphUser represents a user returned from the Microsoft Graph API.
type GraphUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// GetUserByEmail looks up a user by email address (or UPN) via the Graph API.
func (c *GraphClient) GetUserByEmail(ctx context.Context, email string) (*GraphUser, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("empty email address")
	}

	endpoint := "https://graph.microsoft.com/v1.0/users/" + url.PathEscape(email) + "?$select=id,displayName,mail,userPrincipalName"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, fmt.Errorf("graph user lookup failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var user GraphUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ListDirectoryUsers lists users in the organization directory.
func (c *GraphClient) ListDirectoryUsers(ctx context.Context) ([]GraphUser, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}

	var allUsers []GraphUser
	nextURL := "https://graph.microsoft.com/v1.0/users?$select=id,displayName,mail,userPrincipalName&$top=999"

	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
			resp.Body.Close()
			return nil, fmt.Errorf("graph directory listing failed with status %d: %s", resp.StatusCode, string(snippet))
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var result struct {
			Value    []GraphUser `json:"value"`
			NextLink string     `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}
		allUsers = append(allUsers, result.Value...)
		nextURL = result.NextLink
	}

	return allUsers, nil
}

// SearchPeople searches the authenticated user's people directory.
func (c *GraphClient) SearchPeople(ctx context.Context, query string) ([]GraphUser, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty search query")
	}

	endpoint := "https://graph.microsoft.com/v1.0/me/people?$search=%22" + url.QueryEscape(query) + "%22&$select=id,displayName,scoredEmailAddresses,userPrincipalName"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("ConsistencyLevel", "eventual")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, fmt.Errorf("graph people search failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Value []struct {
			ID                    string `json:"id"`
			DisplayName           string `json:"displayName"`
			UserPrincipalName     string `json:"userPrincipalName"`
			ScoredEmailAddresses []struct {
				Address string `json:"address"`
			} `json:"scoredEmailAddresses"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	users := make([]GraphUser, 0, len(result.Value))
	for _, p := range result.Value {
		mail := ""
		if len(p.ScoredEmailAddresses) > 0 {
			mail = p.ScoredEmailAddresses[0].Address
		}
		users = append(users, GraphUser{
			ID:                p.ID,
			DisplayName:       p.DisplayName,
			Mail:              mail,
			UserPrincipalName: p.UserPrincipalName,
		})
	}
	return users, nil
}
