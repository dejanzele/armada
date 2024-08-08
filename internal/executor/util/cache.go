package util

import (
	batchv1 "k8s.io/api/batch/v1"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	v1 "k8s.io/api/core/v1"

	"github.com/armadaproject/armada/internal/executor/metrics"
)

type PodCache interface {
	Add(pod *v1.Pod)
	AddIfNotExists(pod *v1.Pod) bool
	Update(key string, pod *v1.Pod) bool
	Delete(podId string)
	Get(podId string) *v1.Pod
	GetAll() []*v1.Pod
}

type JobCache interface {
	Add(job *batchv1.Job)
	AddIfNotExists(job *batchv1.Job) bool
	Update(key string, job *batchv1.Job) bool
	Delete(jobId string)
	Get(jobId string) *batchv1.Job
	GetAll() []*batchv1.Job
}

type cacheRecord struct {
	pod    *v1.Pod
	expiry time.Time
}

type jobCacheRecord struct {
	job    *batchv1.Job
	expiry time.Time
}

type mapPodCache struct {
	records       map[string]cacheRecord
	rwLock        sync.RWMutex
	defaultExpiry time.Duration
	sizeGauge     prometheus.Gauge
}

func NewTimeExpiringPodCache(expiry time.Duration, cleanUpInterval time.Duration, metricName string) *mapPodCache {
	cache := &mapPodCache{
		records:       map[string]cacheRecord{},
		rwLock:        sync.RWMutex{},
		defaultExpiry: expiry,
		sizeGauge: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: metrics.ArmadaExecutorMetricsPrefix + metricName + "_cache_size",
				Help: "Number of pods in the pod cache",
			},
		),
	}
	cache.runCleanupLoop(cleanUpInterval)
	return cache
}

func (podCache *mapPodCache) Add(pod *v1.Pod) {
	podId := ExtractPodKey(pod)

	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()

	podCache.records[podId] = cacheRecord{pod: pod.DeepCopy(), expiry: time.Now().Add(podCache.defaultExpiry)}
	podCache.sizeGauge.Set(float64(len(podCache.records)))
}

func (podCache *mapPodCache) AddIfNotExists(pod *v1.Pod) bool {
	podId := ExtractPodKey(pod)

	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()

	existing, ok := podCache.records[podId]
	exists := ok && existing.expiry.After(time.Now())
	if !exists {
		podCache.records[podId] = cacheRecord{pod: pod.DeepCopy(), expiry: time.Now().Add(podCache.defaultExpiry)}
		podCache.sizeGauge.Set(float64(len(podCache.records)))
	}
	return !exists
}

func (podCache *mapPodCache) Update(podId string, pod *v1.Pod) bool {
	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()

	existing, ok := podCache.records[podId]
	exists := ok && existing.expiry.After(time.Now())
	if exists {
		podCache.records[podId] = cacheRecord{pod: pod.DeepCopy(), expiry: time.Now().Add(podCache.defaultExpiry)}
	}
	return ok
}

func (podCache *mapPodCache) Delete(podId string) {
	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()

	_, ok := podCache.records[podId]
	if ok {
		delete(podCache.records, podId)
		podCache.sizeGauge.Set(float64(len(podCache.records)))
	}
}

func (podCache *mapPodCache) Get(podId string) *v1.Pod {
	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()

	record := podCache.records[podId]
	if record.expiry.After(time.Now()) {
		return record.pod.DeepCopy()
	}
	return nil
}

func (podCache *mapPodCache) GetAll() []*v1.Pod {
	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()

	all := make([]*v1.Pod, 0, len(podCache.records))
	now := time.Now()

	for _, c := range podCache.records {
		if c.expiry.After(now) {
			all = append(all, c.pod.DeepCopy())
		}
	}
	return all
}

func (podCache *mapPodCache) deleteExpired() {
	podCache.rwLock.Lock()
	defer podCache.rwLock.Unlock()
	now := time.Now()

	for id, c := range podCache.records {
		if c.expiry.Before(now) {
			delete(podCache.records, id)
		}
	}
	// Set size here, so it also fixes the value if it ever gets out of sync
	podCache.sizeGauge.Set(float64(len(podCache.records)))
}

func (podCache *mapPodCache) runCleanupLoop(interval time.Duration) {
	stop := make(chan bool)
	runtime.SetFinalizer(podCache, func(podCache *mapPodCache) { stop <- true })
	go func() {
		ticker := time.NewTicker(interval)
		for {
			select {
			case <-ticker.C:
				podCache.deleteExpired()
			case <-stop:
				ticker.Stop()
				return
			}
		}
	}()
}

type mapJobCache struct {
	records       map[string]jobCacheRecord
	rwLock        sync.RWMutex
	defaultExpiry time.Duration
	sizeGauge     prometheus.Gauge
}

func NewTimeExpiringJobCache(expiry time.Duration, cleanUpInterval time.Duration, metricName string) *mapJobCache {
	cache := &mapJobCache{
		records:       map[string]jobCacheRecord{},
		rwLock:        sync.RWMutex{},
		defaultExpiry: expiry,
		sizeGauge: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: metrics.ArmadaExecutorMetricsPrefix + metricName + "_cache_size",
				Help: "Number of jobs in the pod cache",
			},
		),
	}
	cache.runCleanupLoop(cleanUpInterval)
	return cache
}

func (jobCache *mapJobCache) Add(job *batchv1.Job) {
	podId := ExtractJobKey(job)

	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()

	jobCache.records[podId] = jobCacheRecord{job: job.DeepCopy(), expiry: time.Now().Add(jobCache.defaultExpiry)}
	jobCache.sizeGauge.Set(float64(len(jobCache.records)))
}

func (jobCache *mapJobCache) AddIfNotExists(job *batchv1.Job) bool {
	podId := ExtractJobKey(job)

	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()

	existing, ok := jobCache.records[podId]
	exists := ok && existing.expiry.After(time.Now())
	if !exists {
		jobCache.records[podId] = jobCacheRecord{job: job.DeepCopy(), expiry: time.Now().Add(jobCache.defaultExpiry)}
		jobCache.sizeGauge.Set(float64(len(jobCache.records)))
	}
	return !exists
}

func (jobCache *mapJobCache) Update(podId string, job *batchv1.Job) bool {
	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()

	existing, ok := jobCache.records[podId]
	exists := ok && existing.expiry.After(time.Now())
	if exists {
		jobCache.records[podId] = jobCacheRecord{job: job.DeepCopy(), expiry: time.Now().Add(jobCache.defaultExpiry)}
	}
	return ok
}

func (jobCache *mapJobCache) Delete(jobId string) {
	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()

	_, ok := jobCache.records[jobId]
	if ok {
		delete(jobCache.records, jobId)
		jobCache.sizeGauge.Set(float64(len(jobCache.records)))
	}
}

func (jobCache *mapJobCache) Get(jobId string) *batchv1.Job {
	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()

	record := jobCache.records[jobId]
	if record.expiry.After(time.Now()) {
		return record.job.DeepCopy()
	}
	return nil
}

func (jobCache *mapJobCache) GetAll() []*batchv1.Job {
	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()

	all := make([]*batchv1.Job, 0, len(jobCache.records))
	now := time.Now()

	for _, c := range jobCache.records {
		if c.expiry.After(now) {
			all = append(all, c.job.DeepCopy())
		}
	}
	return all
}

func (jobCache *mapJobCache) deleteExpired() {
	jobCache.rwLock.Lock()
	defer jobCache.rwLock.Unlock()
	now := time.Now()

	for id, c := range jobCache.records {
		if c.expiry.Before(now) {
			delete(jobCache.records, id)
		}
	}
	// Set size here, so it also fixes the value if it ever gets out of sync
	jobCache.sizeGauge.Set(float64(len(jobCache.records)))
}

func (jobCache *mapJobCache) runCleanupLoop(interval time.Duration) {
	stop := make(chan bool)
	runtime.SetFinalizer(jobCache, func(mapJobCache *mapJobCache) { stop <- true })
	go func() {
		ticker := time.NewTicker(interval)
		for {
			select {
			case <-ticker.C:
				jobCache.deleteExpired()
			case <-stop:
				ticker.Stop()
				return
			}
		}
	}()
}
