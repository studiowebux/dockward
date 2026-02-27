package docker

import (
	"context"
	"fmt"
	"net/url"
)

// ContainerStats holds the computed resource usage for a container.
type ContainerStats struct {
	CPUPercent    float64
	MemoryPercent float64
	MemoryUsage   uint64 // bytes
	MemoryLimit   uint64 // bytes
}

// rawStats is the relevant subset of the Docker stats JSON response.
type rawStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
		Stats struct {
			Cache uint64 `json:"cache"`
		} `json:"stats"`
	} `json:"memory_stats"`
}

// ContainerStats fetches a single-shot resource usage snapshot for the container.
// Uses stream=false so the API returns one JSON object immediately.
func (c *Client) ContainerStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	path := "/containers/" + url.PathEscape(containerID) + "/stats?stream=false&one-shot=true"
	data, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("container stats %s: %w", containerID, err)
	}

	var raw rawStats
	if err := decodeJSON(data, &raw); err != nil {
		return nil, fmt.Errorf("decode stats %s: %w", containerID, err)
	}

	// CPU % — mirrors docker stats calculation.
	cpuDelta := float64(raw.CPUStats.CPUUsage.TotalUsage) - float64(raw.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(raw.CPUStats.SystemCPUUsage) - float64(raw.PreCPUStats.SystemCPUUsage)
	numCPUs := raw.CPUStats.OnlineCPUs
	if numCPUs == 0 {
		numCPUs = len(raw.CPUStats.CPUUsage.PercpuUsage)
	}
	var cpuPercent float64
	if systemDelta > 0 && numCPUs > 0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(numCPUs) * 100.0
	}

	// Memory — subtract cache for "real" usage.
	usage := raw.MemoryStats.Usage
	if cache := raw.MemoryStats.Stats.Cache; cache < usage {
		usage -= cache
	}
	limit := raw.MemoryStats.Limit
	var memPercent float64
	if limit > 0 {
		memPercent = float64(usage) / float64(limit) * 100.0
	}

	return &ContainerStats{
		CPUPercent:    cpuPercent,
		MemoryPercent: memPercent,
		MemoryUsage:   usage,
		MemoryLimit:   limit,
	}, nil
}
