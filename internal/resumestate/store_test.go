package resumestate

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestSaveAtomically(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "resume.json")
	if err := os.WriteFile(path, []byte("old"), 0666); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
	}
	want := core.ResumeState{Format: core.ResumeStateFormatATrustClientData, Version: core.ResumeStateVersion1, Revision: 3, Data: "state"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("resume state = %#v, want %#v", *got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0640 {
			t.Fatalf("resume state mode = %o, want 640", gotMode)
		}
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".resume.json.tmp-*"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary files = %v, %v", temporary, err)
	}
}
