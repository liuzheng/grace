package grace

import (
	"os"
	"testing"
)

func TestProcessGetwdForChildProcess(t *testing.T) {
	p := Process{}

	ChildProcessWorkingDirectory = ""
	wd, err := p.getwdForChildProcess()
	if err != nil {
		t.Fatal(err)
	}
	actualWd, _ := os.Getwd()
	if wd != actualWd {
		t.Fatalf("Working directory should be actual wd: %s", actualWd)
	}

	ChildProcessWorkingDirectory = "/some/path"
	wd, err = p.getwdForChildProcess()
	if err != nil {
		t.Fatal(err)
	}
	if wd != "/some/path" {
		t.Fatalf("Working directory should be /some/path")
	}

	ChildProcessWorkingDirectory = ""
}
