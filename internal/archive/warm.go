package archive

import "sync"

// warmers is how many files Warm reads at once.
const warmers = 8

// Warm calls read on each of paths, warmers at a time and in their order, and returns
// a func that waits for every call. On a folder that downloads each file as it is
// opened (Google Drive, OneDrive), the files a run is about to read one by one then
// download side by side, and its own reads find them here. Pass only files the run
// reads, with the read it makes: any other is a download nobody asked for.
func Warm(paths []string, read func(string)) (wait func()) {
	next := make(chan string, len(paths))
	for _, p := range paths {
		next <- p
	}
	close(next)
	var wg sync.WaitGroup
	for i := 0; i < min(warmers, len(paths)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range next {
				func() {
					// A read that panics - an untrusted file, outside the panel job's
					// recover - is a read failed: the run's own read of it reports why.
					defer func() { _ = recover() }()
					read(p)
				}()
			}
		}()
	}
	return wg.Wait
}
