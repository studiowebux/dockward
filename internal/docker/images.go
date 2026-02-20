package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NOTE: Image name path parameters (InspectImage, TagImage, RemoveImage) are NOT
// url.PathEscape'd. Docker's API router uses {name:.*} which handles embedded
// slashes (e.g. "localhost:5000/myapp:latest"). Escaping slashes to %2F breaks
// lookups on some Docker versions. This matches Docker SDK behavior.

// ImageInspect holds image details from the inspect endpoint.
type ImageInspect struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	RepoTags    []string `json:"RepoTags"`
}

// InspectImage returns details for a local image.
func (c *Client) InspectImage(ctx context.Context, name string) (*ImageInspect, error) {
	data, err := c.get(ctx, "/images/"+name+"/json")
	if err != nil {
		return nil, fmt.Errorf("inspect image %s: %w", name, err)
	}
	var img ImageInspect
	if err := decodeJSON(data, &img); err != nil {
		return nil, fmt.Errorf("decode image %s: %w", name, err)
	}
	return &img, nil
}

// TagImage tags a local image with a new reference.
// src is the existing image (name:tag or ID), repo and tag are the new reference.
func (c *Client) TagImage(ctx context.Context, src, repo, tag string) error {
	path := fmt.Sprintf("/images/%s/tag?repo=%s&tag=%s",
		src,
		url.QueryEscape(repo),
		url.QueryEscape(tag),
	)
	_, err := c.post(ctx, path, "")
	if err != nil {
		return fmt.Errorf("tag image %s as %s:%s: %w", src, repo, tag, err)
	}
	return nil
}

// PullImage pulls an image from a registry. Returns when the pull completes.
// The image string should include the registry prefix (e.g., localhost:5000/myapp:latest).
func (c *Client) PullImage(ctx context.Context, image string) error {
	ref, tag := parseImageRef(image)
	path := fmt.Sprintf("/images/create?fromImage=%s&tag=%s",
		url.QueryEscape(ref),
		url.QueryEscape(tag),
	)

	// Pull uses a streaming response. We need a client without timeout.
	stream := newStreamClient()
	reqURL := fmt.Sprintf("http://localhost/%s%s", apiVersion, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}

	resp, err := stream.Do(req) // #nosec G704 -- unix socket only, no external network
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	defer resp.Body.Close()

	// Consume the streaming response to completion.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("pull image %s stream: %w", image, err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("pull image %s: HTTP %d", image, resp.StatusCode)
	}
	return nil
}

// RemoveImage removes a local image.
func (c *Client) RemoveImage(ctx context.Context, name string) error {
	return c.delete(ctx, "/images/"+name)
}

// LocalDigest returns the registry digest for a local image.
// It looks through RepoDigests for a matching registry prefix.
func (img *ImageInspect) LocalDigest(registryPrefix string) string {
	for _, d := range img.RepoDigests {
		if strings.HasPrefix(d, registryPrefix) {
			// Format: registry/name@sha256:abc123
			if idx := strings.Index(d, "@"); idx >= 0 {
				return d[idx+1:]
			}
		}
	}
	return ""
}

// parseImageRef splits "registry/name:tag" into ("registry/name", "tag").
func parseImageRef(image string) (string, string) {
	// Handle tag after the last colon, but not port colons.
	// localhost:5000/name:tag -> ref=localhost:5000/name, tag=tag
	lastSlash := strings.LastIndex(image, "/")
	tagPart := image
	if lastSlash >= 0 {
		tagPart = image[lastSlash:]
	}
	if idx := strings.LastIndex(tagPart, ":"); idx >= 0 {
		absIdx := idx
		if lastSlash >= 0 {
			absIdx = lastSlash + idx
		}
		return image[:absIdx], image[absIdx+1:]
	}
	return image, "latest"
}
