package filewatcher

import (
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

var log = logf.Logger.WithName("filewatcher")

// WatchFileForChanges watches the file, fileToWatch, for changes. If the file contents have changed, the pod this
// function is running on will be restarted.
func WatchFileForChanges(fileToWatch string) error {
	log.Info("Starting the file change watcher")

	// Update the file path to watch in case this is a symlink
	fileToWatch, err := filepath.EvalSymlinks(fileToWatch)
	if err != nil {
		return err
	}
	log.Info("Watching file", "file", fileToWatch)

	// Start the file watcher to monitor file changes
	go func() {
		if err := checkForFileChanges(fileToWatch); err != nil {
			log.Error(err, "Error checking for file changes")
		}
	}()

	return nil
}

// checkForFileChanges starts a new file watcher. If the file is changed, the pod running this function will exit.
func checkForFileChanges(path string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if ok && (event.Has(fsnotify.Write) || event.Has(fsnotify.Chmod) || event.Has(fsnotify.Remove)) {
					log.Info("file was modified, exiting...", "file", event.Name)
					os.Exit(0)
				}
			case err, ok := <-watcher.Errors:
				if ok {
					log.Error(err, "file watcher error")
				} else {
					log.Error(err, "failed to retrieve watcher error")
				}
			}
		}
	}()

	if err = watcher.Add(path); err != nil {
		return err
	}

	return nil
}
