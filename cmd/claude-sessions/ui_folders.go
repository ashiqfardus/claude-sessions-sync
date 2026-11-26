package main

import (
	"net/http"
	"os"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/panel"
)

// apiFolders lists one folder's subfolder names for the folder browser, with the
// browser's starting points and the sync folders found on this computer. Read-only; the
// panel's cookie and Host guard is the access control. No path lists the home folder.
func (u *uiServer) apiFolders(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	path := r.URL.Query().Get("path")
	if path == "" {
		path = home
	}
	detected := []panel.Folder{}
	for _, d := range archive.DetectedFolders() {
		p, disp := archiveLabel(d)
		name := disp
		if p != "" {
			name = p + ": " + disp
		}
		detected = append(detected, panel.Folder{Name: name, Path: d})
	}
	sendJSON(w, struct {
		panel.FolderList
		Roots    []panel.Folder `json:"roots"`
		Detected []panel.Folder `json:"detected"`
	}{panel.ListFolders(path, r.URL.Query().Get("hidden") == "1"), panel.Roots(home, archive.DriveRoots()), detected})
}
