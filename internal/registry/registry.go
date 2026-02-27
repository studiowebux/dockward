// Package registry checks a Docker registry (Distribution HTTP API v2)
// for image digest changes.
package registry

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client communicates with a Docker registry over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a registry client for the given base URL (e.g., http://localhost:5000).
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// RemoteDigest returns the current manifest digest for an image tag.
// Uses HEAD request (lightweight, no download).
// image format: "name:tag" (without registry prefix).
func (c *Client) RemoteDigest(ctx context.Context, image string) (string, error) {
	name, tag := parseRef(image)

	url := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, name, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// Accept both Docker v2 and OCI manifest types so the registry
	// can return whichever format the image was pushed with.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))

	resp, err := c.http.Do(req) // #nosec G704 -- localhost registry only
	if err != nil {
		return "", fmt.Errorf("HEAD %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("image %s:%s not found in registry", name, tag)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HEAD %s: HTTP %d", url, resp.StatusCode)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no Docker-Content-Digest header for %s:%s", name, tag)
	}
	return digest, nil
}

// parseRef splits "name:tag" into (name, tag). Defaults tag to "latest".
func parseRef(image string) (string, string) {
	if idx := strings.LastIndex(image, ":"); idx >= 0 {
		return image[:idx], image[idx+1:]
	}
	return image, "latest"
}
