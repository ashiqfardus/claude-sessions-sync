package panel

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

// A folder with the Hidden attribute, as AppData has, is left out unless hidden ones are asked for.
func TestListFoldersHidesHiddenAttribute(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"AppData", "Documents"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := syscall.UTF16PtrFromString(filepath.Join(root, "AppData"))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetFileAttributes(p, syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_DIRECTORY); err != nil {
		t.Fatal(err)
	}
	if got := names(ListFolders(root, false)); !reflect.DeepEqual(got, []string{"Documents"}) {
		t.Errorf("hidden left out: %v", got)
	}
	if got := names(ListFolders(root, true)); !reflect.DeepEqual(got, []string{"AppData", "Documents"}) {
		t.Errorf("hidden asked for: %v", got)
	}
}
