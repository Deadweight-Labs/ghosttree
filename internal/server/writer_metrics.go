package server

import (
	"fmt"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func writeWriterMetrics(w http.ResponseWriter, stats store.RuntimeStats) {
	s := stats.Writer
	writeMetricHeader(w, "ghosttree_writer_enabled", "Whether this Store uses the runtime writer.", "gauge")
	fmt.Fprintf(w, "ghosttree_writer_enabled %d\n", boolMetric(s.Enabled))
	if !s.Enabled {
		return
	}
	for _, metric := range []struct {
		name, help string
		value      any
	}{
		{"running", "Whether the runtime worker is alive.", boolMetric(s.Running)},
		{"draining", "Whether admission is closed and the worker is draining.", boolMetric(s.Draining)},
		{"outstanding_operations", "Accepted operations including active work.", s.OutstandingOperations},
		{"active_operations", "Operations currently executing, including chunk batch members.", s.ActiveOperations},
		{"queued_operations", "Accepted operations waiting for the worker.", s.QueuedOperations},
		{"outstanding_bytes", "Reserved owned payload bytes including active work.", s.OutstandingBytes},
		{"active_bytes", "Reserved owned payload bytes in active work.", s.ActiveBytes},
		{"queued_bytes", "Reserved owned payload bytes waiting for the worker.", s.QueuedBytes},
		{"operations_high_water", "Highest simultaneous accepted operation count.", s.OperationsHighWater},
		{"bytes_high_water", "Highest simultaneous reserved payload bytes.", s.BytesHighWater},
		{"max_operations", "Configured operation capacity including active work.", s.Config.MaxOperations},
		{"max_bytes", "Configured owned payload byte capacity.", s.Config.MaxBytes},
		{"max_batch", "Configured maximum contiguous chunk batch size.", s.Config.MaxBatch},
		{"read_connections", "Configured read-only pool connection limit.", s.Config.ReadConnections},
	} {
		name := "ghosttree_writer_" + metric.name
		writeMetricHeader(w, name, metric.help, "gauge")
		fmt.Fprintf(w, "%s %v\n", name, metric.value)
	}
	for _, metric := range []struct {
		name, help string
		value      uint64
	}{
		{"admitted_total", "Operations accepted by the writer.", s.Admitted},
		{"completed_total", "Accepted operations completed with success or error.", s.Completed},
		{"failed_total", "Accepted operations completed with error.", s.Failed},
		{"commit_errors_total", "Transaction execution attempts ending in error or rollback.", s.CommitErrors},
		{"batches_total", "Contiguous chunk groups executed.", s.Batches},
		{"batched_operations_total", "Chunk operations executed through group commits.", s.BatchedOperations},
		{"fallback_operations_total", "Original chunk operations retried after a failed group.", s.FallbackOperations},
		{"drain_total", "Transitions closing admission and draining the worker.", s.DrainCount},
	} {
		name := "ghosttree_writer_" + metric.name
		writeMetricHeader(w, name, metric.help, "counter")
		fmt.Fprintf(w, "%s %d\n", name, metric.value)
	}
	writeMetricHeader(w, "ghosttree_writer_drain_duration_seconds", "Elapsed drain duration, final after worker exit.", "gauge")
	fmt.Fprintf(w, "ghosttree_writer_drain_duration_seconds %s\n", formatFloat(s.DrainDuration.Seconds()))
	writeMetricHeader(w, "ghosttree_writer_rejected_total", "Operations rejected before admission by fixed reason.", "counter")
	for i, reason := range [...]string{"closed", "operations_full", "bytes_full", "oversized", "invalid_payload", "canceled", "invalid_config"} {
		fmt.Fprintf(w, "ghosttree_writer_rejected_total{reason=\"%s\"} %d\n", reason, s.Rejected[i])
	}
	writeWriterHistogram(w, "ghosttree_writer_queue_wait_seconds", "Time from enqueue to execution.", s.QueueWait, store.WriterDurationBuckets())
	writeWriterHistogram(w, "ghosttree_writer_commit_duration_seconds", "Time executing a domain transaction through commit or rollback, including failed group attempts.", s.CommitDuration, store.WriterDurationBuckets())
	writeWriterHistogram(w, "ghosttree_writer_batch_size", "Number of original operations in each contiguous chunk group.", s.BatchSize, store.WriterBatchBuckets())
	for _, metric := range []struct{ name, help, kind string }{
		{"keys", "Pending or active bookkeeping keys.", "gauge"},
		{"bytes", "Reserved pending or active bookkeeping bytes.", "gauge"},
		{"active", "Whether the single bookkeeping slot is executing.", "gauge"},
		{"dropped_total", "Bookkeeping updates dropped on contention, capacity, close or execution failure.", "counter"},
	} {
		name := "ghosttree_writer_best_effort_" + metric.name
		writeMetricHeader(w, name, metric.help, metric.kind)
		for kind, label := range [...]string{"delivery", "search", "machine"} {
			b := s.BestEffort[kind]
			var value any
			switch metric.name {
			case "keys":
				value = b.Keys
			case "bytes":
				value = b.Bytes
			case "active":
				value = boolMetric(b.Active)
			case "dropped_total":
				value = b.Dropped
			}
			fmt.Fprintf(w, "%s{kind=\"%s\"} %v\n", name, label, value)
		}
	}
	for _, metric := range []struct {
		name  string
		value any
	}{
		{"max_open_connections", stats.Reader.MaxOpenConnections},
		{"open_connections", stats.Reader.OpenConnections},
		{"in_use_connections", stats.Reader.InUse},
		{"idle_connections", stats.Reader.Idle},
	} {
		name := "ghosttree_reader_" + metric.name
		writeMetricHeader(w, name, "Read-only pool connection count.", "gauge")
		fmt.Fprintf(w, "%s %v\n", name, metric.value)
	}
	writeMetricHeader(w, "ghosttree_reader_wait_count_total", "Read-only pool connection waits.", "counter")
	fmt.Fprintf(w, "ghosttree_reader_wait_count_total %d\n", stats.Reader.WaitCount)
	writeMetricHeader(w, "ghosttree_reader_wait_duration_seconds_total", "Read-only pool connection wait duration.", "counter")
	fmt.Fprintf(w, "ghosttree_reader_wait_duration_seconds_total %s\n", formatFloat(stats.Reader.WaitDuration.Seconds()))
}

func writeWriterHistogram(w http.ResponseWriter, name, help string, h store.WriterHistogram, bounds [12]float64) {
	writeMetricHeader(w, name, help, "histogram")
	for i, bound := range bounds {
		fmt.Fprintf(w, "%s_bucket{le=\"%s\"} %d\n", name, formatFloat(bound), h.Buckets[i])
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %s\n%s_count %d\n", name, h.Count, name, formatFloat(h.Sum), name, h.Count)
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}
