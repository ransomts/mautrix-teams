package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ChannelFile is one entry of a channel's Files tab (its SharePoint folder).
type ChannelFile struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
	ModifiedBy string `json:"modified_by,omitempty"`
	WebURL     string `json:"web_url"`
	IsFolder   bool   `json:"is_folder,omitempty"`
	Path       string `json:"path,omitempty"` // folder path under the channel folder, for nested files
	ChildCount int    `json:"child_count,omitempty"`
}

type driveItem struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Size                 int64  `json:"size"`
	WebURL               string `json:"webUrl"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	LastModifiedBy       struct {
		User struct {
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"lastModifiedBy"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
	ParentReference struct {
		DriveID string `json:"driveId"`
	} `json:"parentReference"`
}

func (c *GraphClient) getJSON(ctx context.Context, endpoint string, out any) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return ErrMissingGraphAccessToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return GraphUploadError{Status: resp.StatusCode, BodySnippet: string(snippet)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// graphBaseURL is overridable for tests.
var graphBaseURL = "https://graph.microsoft.com/v1.0"

// ListChannelFiles lists a channel's Files tab: the files and folders in its
// SharePoint folder, and those one folder down, newest first within each.
func (c *GraphClient) ListChannelFiles(ctx context.Context, teamID, channelID string) ([]ChannelFile, error) {
	var folder driveItem
	endpoint := fmt.Sprintf("%s/teams/%s/channels/%s/filesFolder", graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
	if err := c.getJSON(ctx, endpoint, &folder); err != nil {
		return nil, err
	}
	driveID := folder.ParentReference.DriveID
	if driveID == "" || folder.ID == "" {
		return nil, fmt.Errorf("the channel has no files folder")
	}
	top, err := c.listChildren(ctx, driveID, folder.ID)
	if err != nil {
		return nil, err
	}
	var files []ChannelFile
	for _, item := range top {
		files = append(files, toChannelFile(item, ""))
		if item.Folder != nil && item.Folder.ChildCount > 0 {
			children, err := c.listChildren(ctx, driveID, item.ID)
			if err != nil {
				continue
			}
			for _, child := range children {
				files = append(files, toChannelFile(child, item.Name))
			}
		}
	}
	return files, nil
}

func (c *GraphClient) listChildren(ctx context.Context, driveID, itemID string) ([]driveItem, error) {
	endpoint := fmt.Sprintf("%s/drives/%s/items/%s/children?$top=200",
		graphBaseURL, url.PathEscape(driveID), url.PathEscape(itemID))
	var page struct {
		Value []driveItem `json:"value"`
	}
	if err := c.getJSON(ctx, endpoint, &page); err != nil {
		return nil, err
	}
	// Newest first (RFC 3339 times sort as strings).
	sort.SliceStable(page.Value, func(i, j int) bool {
		return page.Value[i].LastModifiedDateTime > page.Value[j].LastModifiedDateTime
	})
	return page.Value, nil
}

func toChannelFile(item driveItem, path string) ChannelFile {
	f := ChannelFile{
		Name:       item.Name,
		Size:       item.Size,
		Modified:   item.LastModifiedDateTime,
		ModifiedBy: item.LastModifiedBy.User.DisplayName,
		WebURL:     item.WebURL,
		IsFolder:   item.Folder != nil,
		Path:       path,
	}
	if item.Folder != nil {
		f.ChildCount = item.Folder.ChildCount
	}
	return f
}
