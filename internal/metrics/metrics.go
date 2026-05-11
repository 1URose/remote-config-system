package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type Registry struct {
	successfulUpdates atomic.Int64
	failedUpdates     atomic.Int64
	versionConflicts  atomic.Int64
	flushRequests     atomic.Int64
	reloadRequests    atomic.Int64
	redisConnected    atomic.Int64
	updateDurationNS  atomic.Int64
	updateDurationCnt atomic.Int64
}

func NewRegistry() *Registry {
	r := &Registry{}
	r.redisConnected.Store(1)
	return r
}

func (r *Registry) IncSuccessfulUpdates() { r.successfulUpdates.Add(1) }
func (r *Registry) IncFailedUpdates()     { r.failedUpdates.Add(1) }
func (r *Registry) IncVersionConflicts()  { r.versionConflicts.Add(1) }
func (r *Registry) IncFlushRequests()     { r.flushRequests.Add(1) }
func (r *Registry) IncReloadRequests()    { r.reloadRequests.Add(1) }

func (r *Registry) ObserveUpdateDuration(d time.Duration) {
	r.updateDurationNS.Add(d.Nanoseconds())
	r.updateDurationCnt.Add(1)
}

func (r *Registry) SetRedisConnected(connected bool) {
	if connected {
		r.redisConnected.Store(1)
		return
	}
	r.redisConnected.Store(0)
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		totalDuration := r.updateDurationNS.Load()
		totalCount := r.updateDurationCnt.Load()
		avgDuration := 0.0
		if totalCount > 0 {
			avgDuration = time.Duration(totalDuration).Seconds() / float64(totalCount)
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w,
			"remote_config_successful_updates_total %d\n"+
				"remote_config_failed_updates_total %d\n"+
				"remote_config_version_conflicts_total %d\n"+
				"remote_config_flush_requests_total %d\n"+
				"remote_config_reload_requests_total %d\n"+
				"remote_config_redis_connected %d\n"+
				"remote_config_update_duration_seconds_avg %.6f\n",
			r.successfulUpdates.Load(),
			r.failedUpdates.Load(),
			r.versionConflicts.Load(),
			r.flushRequests.Load(),
			r.reloadRequests.Load(),
			r.redisConnected.Load(),
			avgDuration,
		)
	})
}
