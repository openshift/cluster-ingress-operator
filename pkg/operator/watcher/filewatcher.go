package watcher

import (
	"fmt"
	"io/ioutil"
	"sync"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"gopkg.in/fsnotify.v1"
)

var log = logf.Logger.WithName("filewatcher")

// FileWatcher watches a file for changes.
type FileWatcher struct {
	sync.Mutex
	file        string
	currentData []byte
	watcher     *fsnotify.Watcher
}

// New returns a new FileWatcher watching the given file.
func New(file string) (*FileWatcher, error) {
	var err error

	cw := &FileWatcher{
		file: file,
	}

	// Initial read of file.
	if err := cw.ReadFile(); err != nil {
		return nil, err
	}

	cw.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return cw, nil
}

// GetFile fetches the file being watched by FileWatcher.
func (fw *FileWatcher) GetFile() string {
	fw.Lock()
	defer fw.Unlock()
	return fw.file
}

// GetFileData fetches the currently loaded file data of FileWatcher.
func (fw *FileWatcher) GetFileData() []byte {
	fw.Lock()
	defer fw.Unlock()
	return fw.currentData
}

// Start starts the FileWatcher.
func (fw *FileWatcher) Start(stopCh <-chan struct{}) error {
	if err := fw.watcher.Add(fw.file); err != nil {
		return err
	}

	go fw.Watch()

	log.Info("Starting file watcher")

	// Block until the stop channel is closed.
	<-stopCh

	return fw.watcher.Close()
}

// Watch reads events from the watcher's channel and reacts to changes.
func (fw *FileWatcher) Watch() {
	for {
		select {
		case event, ok := <-fw.watcher.Events:
			// Channel is closed.
			if !ok {
				return
			}

			fw.handleEvent(event)

		case err, ok := <-fw.watcher.Errors:
			// Channel is closed.
			if !ok {
				return
			}

			log.Error(err, "file watch error")
		}
	}
}

// ReadFile reads the watched file from disk, parses the file,
// and updates FileWatcher current data.
func (fw *FileWatcher) ReadFile() error {
	data, err := ioutil.ReadFile(fw.file)
	switch {
	case err != nil:
		return fmt.Errorf("failed to read file %s: %v", fw.file, err)
	case len(data) == 0:
		return fmt.Errorf("file %s contains no currentData", fw.file)
	}

	fw.Lock()
	fw.currentData = data
	fw.Unlock()

	log.Info("updated file watcher current data")

	return nil
}

// handleEvent filters events, re-adds and re-reads the watched file
// if removed.
func (fw *FileWatcher) handleEvent(event fsnotify.Event) {
	if !(isWrite(event) || isRemove(event) || isCreate(event)) {
		return
	}

	log.Info("watched file change", "event", event)

	if isRemove(event) {
		if err := fw.watcher.Add(event.Name); err != nil {
			log.Error(err, "error re-watching file %s", fw.file)
		}
	}

	if err := fw.ReadFile(); err != nil {
		log.Error(err, "error re-reading watched file %s", fw.file)
	}
}

func isWrite(event fsnotify.Event) bool {
	return event.Op&fsnotify.Write == fsnotify.Write
}

func isCreate(event fsnotify.Event) bool {
	return event.Op&fsnotify.Create == fsnotify.Create
}

func isRemove(event fsnotify.Event) bool {
	return event.Op&fsnotify.Remove == fsnotify.Remove
}
