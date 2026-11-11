package main

import "testing"

func TestPercentilesAndMean(t *testing.T) {
	v := []float64{5, 1, 3, 2, 4}
	if got := pct(v, 50); got != 3 {
		t.Errorf("p50 = %v", got)
	}
	if got := pct(v, 100); got != 5 {
		t.Errorf("max = %v", got)
	}
	if got := mean(v); got != 3 {
		t.Errorf("mean = %v", got)
	}
	if pct(nil, 95) != 0 || mean(nil) != 0 {
		t.Error("empty input must give 0")
	}
}

// A call that did not reach a phase counts as zero for it: the figures are those
// of a call, not of the calls that happened to run the phase.
func TestLogSinkPhasesCountsMissingPhasesAsZero(t *testing.T) {
	l := &logSink{}
	l.Write([]byte(`{"event":"http_request","route":"/r","duration_ms":10,"phases":{"a":4,"b":2}}` + "\n"))
	l.Write([]byte(`{"event":"http_request","route":"/r","duration_ms":6,"phases":{"a":1}}` + "\n"))
	l.Write([]byte(`{"event":"http_request","route":"/other","duration_ms":99,"phases":{"a":9}}` + "\n"))
	l.Write([]byte(`not json` + "\n"))
	got := l.phases("/r")
	if len(got["a"]) != 2 || len(got["b"]) != 2 || got["b"][1] != 0 || got["total"][0] != 10 || got["total"][1] != 6 {
		t.Fatalf("phases = %v", got)
	}
}
