package watcher

import (
	"fmt"
	"io/ioutil"
	"sync"

	"gopkg.in/fsnotify.v1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

var log = logf.Logger.WithName("filewatcher")

// FileWatcher watches a file for changes.
type FileWatcher struct {
	sync.Mutex
	fileName    string
	currentData []byte
	watcher     *fsnotify.Watcher
}

// New returns a new FileWatcher watching the given file.
func New(file string) (*FileWatcher, error) {
	fw := &FileWatcher{
		fileName: file,
	}

	// Initial read of file.
	if err := fw.ReadFile(); err != nil {
		return nil, err
	}

	if watcher, err := fsnotify.NewWatcher(); err != nil {
		return nil, err
	} else {
		fw.watcher = watcher
	}

	return fw, nil
}

// GetFileData fetches the data of the currently watched file.
func (fw *FileWatcher) GetFileData() []byte {
	fw.Lock()
	defer fw.Unlock()
	return fw.currentData
}

// Start starts the FileWatcher.
func (fw *FileWatcher) Start(stopCh <-chan struct{}, reloadCh chan struct{}) error {
	if err := fw.watcher.Add(fw.fileName); err != nil {
		return err
	}

	go fw.Watch(reloadCh)
	log.Info("starting file watcher")
	// Block until the stop channel is closed.
	<-stopCh

	return fw.watcher.Close()
}

// Watch reads events from the watcher's channel and reacts to changes.
func (fw *FileWatcher) Watch(reload chan struct{}) {
	for {
		select {
		case event, ok := <-fw.watcher.Events:
			if !ok {
				log.Info("file watch events channel closed")
				return
			}
			fw.handleEvent(event, reload)

		case err, ok := <-fw.watcher.Errors:
			if !ok {
				log.Info("file watch error channel closed")
				return
			}
			log.Error(err, "file watch error")
		}
	}
}

// ReadFile reads the watched file from disk, parses the file,
// and updates FileWatcher current data.
func (fw *FileWatcher) ReadFile() error {
	data, err := ioutil.ReadFile(fw.fileName)
	switch {
	case err != nil:
		return fmt.Errorf("failed to read file %s: %v", fw.fileName, err)
	case len(data) == 0:
		return fmt.Errorf("file %s contains no data", fw.fileName)
	}
	fw.Lock()
	fw.currentData = data
	fw.Unlock()
	log.Info("reloaded file watcher current data")

	return nil
}

// handleEvent filters events, re-adds and re-reads the watched file
// if removed.
func (fw *FileWatcher) handleEvent(event fsnotify.Event, reload chan struct{}) {
	if !(isWrite(event) || isRemove(event) || isCreate(event)) {
		return
	}
	log.Info("watched file change", "event", event)

	if isRemove(event) {
		if err := fw.watcher.Add(event.Name); err != nil {
			log.Error(err, "error re-watching file %s", fw.fileName)
		}
	}
	if err := fw.ReadFile(); err != nil {
		log.Error(err, "error re-reading watched file %s", fw.fileName)
	} else {
		reload <- struct{}{}
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
