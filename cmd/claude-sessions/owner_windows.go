package main

import "os"

// ownedByMe is always true on Windows, where each user has a temp folder of their own.
func ownedByMe(os.FileInfo) bool { return true }
