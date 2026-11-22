package archive

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeHome points the home directory at a temp folder for every OS's lookup, and
// hides this machine's drives: on Windows a real G:\My Drive would be found first.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	saved := driveRoots
	driveRoots = func() []string { return nil }
	t.Cleanup(func() { driveRoots = saved })
	return home
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func detected(t *testing.T) string {
	t.Helper()
	dest, _, err := ResolveDestination(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

// Current Drive for desktop on macOS mounts under ~/Library/CloudStorage, not
// ~/Google Drive (#7).
func TestDetectsGoogleDriveUnderCloudStorage(t *testing.T) {
	home := fakeHome(t)
	want := filepath.Join(home, "Library", "CloudStorage", "GoogleDrive-me@example.com", "My Drive", archiveFolderName)
	mkdirs(t, want)

	if got := detected(t); got != want {
		t.Fatalf("detected %q, want %q", got, want)
	}
}

func TestDetectsOneDriveUnderCloudStorage(t *testing.T) {
	home := fakeHome(t)
	want := filepath.Join(home, "Library", "CloudStorage", "OneDrive-Personal", archiveFolderName)
	mkdirs(t, want)

	if got := detected(t); got != want {
		t.Fatalf("detected %q, want %q", got, want)
	}
}

// Two accounts that both hold an archive: picking one is a guess, so pick none.
func TestDetectRefusesTwoCloudStorageArchives(t *testing.T) {
	home := fakeHome(t)
	cs := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t,
		filepath.Join(cs, "GoogleDrive-a@example.com", "My Drive", archiveFolderName),
		filepath.Join(cs, "GoogleDrive-b@example.com", "My Drive", archiveFolderName))

	if got := detected(t); got != "" {
		t.Fatalf("detected %q, want none when two accounts hold an archive", got)
	}
}

// Only one of two accounts holds an archive: that one is not a guess.
func TestDetectUsesTheOnlyAccountWithAnArchive(t *testing.T) {
	home := fakeHome(t)
	cs := filepath.Join(home, "Library", "CloudStorage")
	want := filepath.Join(cs, "GoogleDrive-b@example.com", "My Drive", archiveFolderName)
	mkdirs(t, filepath.Join(cs, "GoogleDrive-a@example.com", "My Drive"), want)

	if got := detected(t); got != want {
		t.Fatalf("detected %q, want %q", got, want)
	}
}

// No archive yet: a single mounted account is where the first push creates one.
func TestDetectFallsBackToTheOnlyMountedDrive(t *testing.T) {
	home := fakeHome(t)
	parent := filepath.Join(home, "Library", "CloudStorage", "GoogleDrive-me@example.com", "My Drive")
	mkdirs(t, parent)

	want := filepath.Join(parent, archiveFolderName)
	if got := detected(t); got != want {
		t.Fatalf("detected %q, want %q", got, want)
	}
}

func TestDetectFallbackRefusesTwoMountedDrives(t *testing.T) {
	home := fakeHome(t)
	cs := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t,
		filepath.Join(cs, "GoogleDrive-a@example.com", "My Drive"),
		filepath.Join(cs, "GoogleDrive-b@example.com", "My Drive"))

	if got := detected(t); got != "" {
		t.Fatalf("detected %q, want none when two accounts are mounted", got)
	}
}

// Drive for desktop on Windows mounts as <letter>:\My Drive: the archive in it is found,
// and before there is one, it is created there. A temp folder stands in for the drive.
func TestDetectsMyDriveOnADriveRoot(t *testing.T) {
	fakeHome(t)
	root := t.TempDir()
	driveRoots = func() []string { return []string{root} }
	want := filepath.Join(root, "My Drive", archiveFolderName)

	mkdirs(t, filepath.Dir(want))
	if got := detected(t); got != want {
		t.Fatalf("no archive yet: detected %q, want %q", got, want)
	}
	mkdirs(t, want)
	if got := detected(t); got != want {
		t.Fatalf("detected %q, want %q", got, want)
	}
}
