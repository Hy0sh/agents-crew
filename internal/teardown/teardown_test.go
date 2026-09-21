package teardown

import "testing"

func TestWorkerDirNamePattern(t *testing.T) {
	cases := map[string]bool{
		"worker1-20260921181008": true,
		"worker12-abc":           true,
		"worker3-pr1631":         true,
		"worker-nostamp":         false, // no digit right after "worker"
		"worker1":                false, // no "-suffix" at all
		"master":                 false,
		"synchronous-nibbling":   false,
	}
	for name, want := range cases {
		if got := workerDirName.MatchString(name); got != want {
			t.Errorf("workerDirName.MatchString(%q) = %v, want %v", name, got, want)
		}
	}
}
