package metrics

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// MetricsCache holds cached metrics data.
type MetricsCache struct {
	mu         sync.RWMutex
	podMetrics  map[string]*models.MetricPoint // key: "namespace/name"
	nodeMetrics map[string]*models.MetricPoint // key: node name
}

// Collector polls the Kubernetes Metrics Server.
type Collector struct {
	metricsClient metricsv.Interface
	cache         *MetricsCache
	available     bool
	mu            sync.RWMutex
	interval      time.Duration
}

// NewCollector creates a new metrics collector.
func NewCollector(metricsClient metricsv.Interface, interval time.Duration) *Collector {
	return &Collector{
		metricsClient: metricsClient,
		cache: &MetricsCache{
			podMetrics:  make(map[string]*models.MetricPoint),
			nodeMetrics: make(map[string]*models.MetricPoint),
		},
		interval: interval,
	}
}

// Poll runs a single metrics collection cycle. Safe to call before Start.
func (c *Collector) Poll(ctx context.Context) {
	if c.metricsClient == nil {
		return
	}
	c.poll(ctx)
}

// Start begins the polling loop. Should be called as a goroutine.
func (c *Collector) Start(ctx context.Context) {
	if c.metricsClient == nil {
		log.Println("Metrics client not available, collector disabled")
		return
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.poll(ctx)
		}
	}
}

func (c *Collector) poll(ctx context.Context) {
	c.pollNodeMetrics(ctx)
	c.pollPodMetrics(ctx)
}

func (c *Collector) pollNodeMetrics(ctx context.Context) {
	nodeMetrics, err := c.metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.mu.Lock()
		c.available = false
		c.mu.Unlock()
		log.Printf("Failed to fetch node metrics: %v", err)
		return
	}

	c.mu.Lock()
	c.available = true
	c.mu.Unlock()

	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()

	for _, nm := range nodeMetrics.Items {
		c.cache.nodeMetrics[nm.Name] = &models.MetricPoint{
			Timestamp: nm.Timestamp.Time,
			Resource:  nm.Name,
			CPUUsage:  nm.Usage.Cpu().MilliValue(),
			MemUsage:  nm.Usage.Memory().Value(),
		}
	}
}

func (c *Collector) pollPodMetrics(ctx context.Context) {
	podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("Failed to fetch pod metrics: %v", err)
		return
	}

	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()

	for _, pm := range podMetrics.Items {
		key := fmt.Sprintf("%s/%s", pm.Namespace, pm.Name)
		var cpuTotal, memTotal int64
		for _, container := range pm.Containers {
			cpuTotal += container.Usage.Cpu().MilliValue()
			memTotal += container.Usage.Memory().Value()
		}
		c.cache.podMetrics[key] = &models.MetricPoint{
			Timestamp: pm.Timestamp.Time,
			Resource:  key,
			CPUUsage:  cpuTotal,
			MemUsage:  memTotal,
		}
	}
}

// IsAvailable returns whether the metrics server is reachable.
func (c *Collector) IsAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.available
}

// GetPodMetrics returns metrics for a specific pod.
func (c *Collector) GetPodMetrics(namespace, name string) *models.MetricPoint {
	key := fmt.Sprintf("%s/%s", namespace, name)
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()
	return c.cache.podMetrics[key]
}

// GetNodeMetrics returns metrics for a specific node.
func (c *Collector) GetNodeMetrics(name string) *models.MetricPoint {
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()
	return c.cache.nodeMetrics[name]
}

// GetAllPodMetrics returns all cached pod metrics.
func (c *Collector) GetAllPodMetrics() map[string]*models.MetricPoint {
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()
	result := make(map[string]*models.MetricPoint, len(c.cache.podMetrics))
	for k, v := range c.cache.podMetrics {
		result[k] = v
	}
	return result
}

// GetAllNodeMetrics returns all cached node metrics.
func (c *Collector) GetAllNodeMetrics() map[string]*models.MetricPoint {
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()
	result := make(map[string]*models.MetricPoint, len(c.cache.nodeMetrics))
	for k, v := range c.cache.nodeMetrics {
		result[k] = v
	}
	return result
}
